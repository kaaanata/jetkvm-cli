#include "libavcodec/avcodec.h"
#include "libavutil/imgutils.h"
#include "libavutil/log.h"
#include <stdint.h>
#include <string.h>
static AVCodecContext* decoder;
static AVFrame* frame;
static uint8_t input[(8<<20)+AV_INPUT_BUFFER_PADDING_SIZE];
static uint32_t output[9];
static int draining;
__attribute__((export_name("input_ptr"))) uint32_t input_ptr(void){return (uintptr_t)input;}
__attribute__((export_name("output_ptr"))) uint32_t output_ptr(void){return (uintptr_t)output;}
__attribute__((export_name("decode"))) int decode(uint32_t length,uint64_t token,uint32_t drain){
 if(length>(8<<20))return -1;
 if(!decoder){
  av_log_set_level(AV_LOG_QUIET);
  decoder=avcodec_alloc_context3(avcodec_find_decoder(AV_CODEC_ID_H264));frame=av_frame_alloc();
  if(!decoder||!frame)return -2;
  decoder->thread_count=1;decoder->thread_type=0;decoder->max_pixels=4096*2160;
  decoder->err_recognition=AV_EF_EXPLODE|AV_EF_BITSTREAM|AV_EF_BUFFER;decoder->error_concealment=0;
  if(avcodec_open2(decoder,decoder->codec,NULL)<0)return -3;
 }
 int rc=0;
 if(length){AVPacket packet={0};packet.data=input;packet.size=length;packet.pts=(int64_t)token;packet.dts=AV_NOPTS_VALUE;memset(input+length,0,AV_INPUT_BUFFER_PADDING_SIZE);rc=avcodec_send_packet(decoder,&packet);if(rc<0)return rc;}
 if(!length&&drain&&!draining){rc=avcodec_send_packet(decoder,NULL);if(rc<0)return rc;draining=1;}
 av_frame_unref(frame);rc=avcodec_receive_frame(decoder,frame);
 if(rc==AVERROR(EAGAIN)&&drain&&!draining){rc=avcodec_send_packet(decoder,NULL);if(rc<0)return rc;draining=1;rc=avcodec_receive_frame(decoder,frame);}
 memset(output,0,sizeof(output));
 if(rc==AVERROR(EAGAIN)||rc==AVERROR_EOF)return 0;
 if(rc<0)return rc;
 if(frame->decode_error_flags||frame->format!=AV_PIX_FMT_YUV420P||frame->width<=0||frame->height<=0||frame->width>4096||frame->height>2160)return -4;
 output[0]=frame->width;output[1]=frame->height;output[2]=frame->linesize[0];output[3]=frame->linesize[1];
 for(int i=0;i<3;i++)output[4+i]=(uintptr_t)frame->data[i];
 output[7]=(uint32_t)frame->pts;output[8]=(uint32_t)((uint64_t)frame->pts>>32);
 return 0;
}
