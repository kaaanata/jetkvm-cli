package jetkvm

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

const (
	signalingPath       = "/webrtc/signaling/client"
	maxSignalingMessage = 1 << 20
)

var (
	ErrSignalingFailed         = errors.New("JetKVM signaling failed")
	ErrLegacySignaling         = errors.New("JetKVM does not support WebSocket signaling")
	ErrInvalidSignalingMessage = errors.New("invalid JetKVM signaling message")
)

type signalMessage struct {
	Type  string         `json:"type,omitempty"`
	Data  jsontext.Value `json:"data,omitempty"`
	Error jsontext.Value `json:"error,omitempty"`
}

type signalingConn interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	Close(websocket.StatusCode, string) error
	CloseNow() error
	SetReadLimit(int64)
}

type signalSocket struct {
	conn    signalingConn
	writeMu sync.Mutex
}

func (s *signalSocket) read(ctx context.Context) (signalMessage, error) {
	for {
		messageType, payload, err := s.conn.Read(ctx)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return signalMessage{}, cause
			}
			return signalMessage{}, ErrSignalingFailed
		}
		if messageType != websocket.MessageText {
			continue
		}
		if string(payload) == "ping" {
			if err := s.writeText(ctx, []byte("pong")); err != nil {
				return signalMessage{}, err
			}
			continue
		}

		var message signalMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			return signalMessage{}, ErrInvalidSignalingMessage
		}
		return message, nil
	}
}

func (s *signalSocket) write(ctx context.Context, message signalMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return ErrInvalidSignalingMessage
	}
	return s.writeText(ctx, payload)
}

func (s *signalSocket) writeText(ctx context.Context, payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return ErrSignalingFailed
	}
	return nil
}

func (c *Client) dialSignaling(ctx context.Context) (*signalSocket, error) {
	conn, status, err := c.dialSignalingOnce(ctx)
	if err == nil {
		return conn, nil
	}
	if status != http.StatusUnauthorized {
		return nil, err
	}

	c.authMu.Lock()
	defer c.authMu.Unlock()

	conn, status, err = c.dialSignalingOnce(ctx)
	if err == nil {
		return conn, nil
	}
	if status != http.StatusUnauthorized {
		return nil, err
	}
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	conn, _, err = c.dialSignalingOnce(ctx)
	return conn, err
}

func (c *Client) dialSignalingOnce(ctx context.Context) (*signalSocket, int, error) {
	endpoint := c.origin.Clone()
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = signalingPath

	header := make(http.Header)
	header.Set("Origin", c.origin.String())
	conn, response, err := websocket.Dial(ctx, endpoint.String(), &websocket.DialOptions{
		HTTPClient: c.http,
		HTTPHeader: header,
	})
	status := 0
	if response != nil {
		status = response.StatusCode
		if response.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
			_ = response.Body.Close()
		}
	}
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return nil, status, cause
		}
		if status == http.StatusUnauthorized {
			return nil, status, &HTTPStatusError{StatusCode: status}
		}
		return nil, status, ErrSignalingFailed
	}
	conn.SetReadLimit(maxSignalingMessage)
	return &signalSocket{conn: conn}, status, nil
}

func rawJSON(value any) (jsontext.Value, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsontext.Value(payload), nil
}
