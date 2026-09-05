package onboarding

import (
	"fmt"
	"html/template"
	"net/http"
	"slices"
)

type SettingChange struct{ Field, Before, After string }

func SettingChanges(before, after Settings) []SettingChange {
	var changes []SettingChange
	add := func(field string, a, b any) {
		if fmt.Sprint(a) != fmt.Sprint(b) {
			changes = append(changes, SettingChange{field, fmt.Sprint(a), fmt.Sprint(b)})
		}
	}
	add("Default output", before.Output, after.Output)
	add("Global keyboard and mouse permission", before.InputEnabled, after.InputEnabled)
	for _, old := range before.Devices {
		for _, next := range after.Devices {
			if old.DeviceID != next.DeviceID {
				continue
			}
			prefix := old.Name + " (" + old.Origin + ") — "
			add(prefix+"exposed to agents", old.Exposed, next.Exposed)
			add(prefix+"keyboard and mouse permission", slices.Contains(old.Permissions, "input"), slices.Contains(next.Permissions, "input"))
			add(prefix+"session takeover allowed", old.TakeoverAllowed, next.TakeoverAllowed)
			add(prefix+"idle timeout", old.IdleTimeout, next.IdleTimeout)
			add(prefix+"absolute lifetime", old.AbsoluteLifetime, next.AbsoluteLifetime)
		}
	}
	return changes
}

func (b *Browser) serveUpdate(w http.ResponseWriter, r *http.Request, s *browserSession) {
	s.mu.Lock()
	done := s.progress.Receipt != nil
	s.mu.Unlock()
	message := ""
	if r.Method == "POST" && !done {
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("approve") != "yes" {
			message = "Review and approve these changes to continue."
		} else {
			receipt, err := b.service.Update(r.Context(), *s.patch)
			if err != nil {
				message = PublicMessage(err)
			} else {
				s.mu.Lock()
				s.progress.Status = "updated"
				s.progress.Message = "Configuration saved and active. Return to your agent or terminal."
				s.progress.URL = ""
				s.progress.Receipt = &receipt
				s.mu.Unlock()
				done = true
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = settingsPage.Execute(w, struct {
		Changes []SettingChange
		Message string
		Done    bool
	}{s.changes, message, done})
}

var settingsPage = template.Must(template.New("settings").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Review JetKVM settings</title>
<style>body{font:17px system-ui;color:#e8edf4;background:#111820;padding:30px 20px}main{max-width:640px;margin:4vh auto}p{line-height:1.5;color:#aab8c8}article{padding:14px 0;border-bottom:1px solid #435264}strong{display:block}del{color:#ffb4a9}ins{color:#b0e4bc;text-decoration:none}button{display:block;margin-top:24px;padding:13px 22px;background:#83c9f4;border:0;border-radius:7px;font:inherit}label{display:block;margin-top:24px}.error{color:#ffb4a9}</style>
<main>{{if .Done}}<h1>Settings updated</h1><p>Return to your agent or terminal. Your saved configuration is active. No input or power action was sent.</p>{{else}}<h1>Review settings changes</h1><p>These are the exact changes requested by your agent or command. Existing device identities, passwords, power permissions, and confirmation requirements will not change.</p>
{{range .Changes}}<article><strong>{{.Field}}</strong><del>{{.Before}}</del> → <ins>{{.After}}</ins></article>{{else}}<p>The requested values are already configured.</p>{{end}}
{{if .Message}}<p class="error" role="alert">{{.Message}}</p>{{end}}
<form method="post"><label><input type="checkbox" name="approve" value="yes" required> Apply these configuration changes.</label><button type="submit">Save settings</button></form><p>Close this page to cancel. The link expires in 10 minutes.</p>{{end}}</main></html>`))
