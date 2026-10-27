package cryptoaudio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	gosilk "github.com/wdvxdr1123/go-silk"
)

// SILK 语音解码适配：微信语音 silk（0x02#!SILK_V3 / #!SILK_V3 头）→ PCM s16le → WAV。
// 默认后端 github.com/wdvxdr1123/go-silk（ccgo 转写 SILK SDK，纯 Go 无 CGO；与
// crane-answer silk-wasm 解码口径对齐——silk-wasm v3 decode 按入参采样率输出、微信语音
// 常规 24kHz，见 crane-answer/src/wechatpadpro/fileStore.js）。可经 SetSilkDecoder 注入替换
// （如 cgo 版实现；CGO 风险见包注释）。
const (
	// DefaultSilkSampleRate 微信语音 silk 常规采样率（与 silk-wasm SILK_DECODE_RATE 同口径）
	DefaultSilkSampleRate = 24000
	// SilkDecodeRate 同 DefaultSilkSampleRate（适配层别名）
	SilkDecodeRate = DefaultSilkSampleRate
	// silkMagic 微信语音 SILK 流魔数
	silkMagic = "#!SILK_V3"
)

// 错误口径（对齐 Node convertVoice 与 silk-wasm 空结果）
var (
	// ErrNoSilkDecoder 未注册 SILK 解码器
	ErrNoSilkDecoder = errors.New("SILK 解码器未注册")
	// ErrUnknownAudio 无法识别的音频编码
	ErrUnknownAudio = errors.New("语音编码无法识别（非 wav/mp3/silk）")
	// ErrEmptyVoice 空语音内容
	ErrEmptyVoice = errors.New("语音内容为空")
	// ErrEmptySilkDecode SILK 解码结果为空
	ErrEmptySilkDecode = errors.New("silk 解码结果为空")
)

// AudioFormat 探测出的音频格式
type AudioFormat string

const (
	FormatWAV  AudioFormat = "wav"
	FormatMP3  AudioFormat = "mp3"
	FormatSILK AudioFormat = "silk"
	FormatNone AudioFormat = ""
)

// SilkDecoder SILK → PCM16 解码后端抽象（可替换为 cgo 版 go-silk 等实现）
type SilkDecoder interface {
	// DecodeSilk 解码 SILK 流为 PCM16LE 单声道，sampleRate 为输出采样率
	DecodeSilk(data []byte, sampleRate int) ([]byte, error)
}

// SilkEncoder PCM16 → SILK 编码后端抽象（测试素材生成与互转用）
type SilkEncoder interface {
	// EncodeSilk 编码 PCM16LE 单声道为 SILK 流
	EncodeSilk(data []byte, sampleRate int) ([]byte, error)
}

// SilkDecoderFunc / SilkDecoderFunc 函数式实现（便于注入测试桩）
type SilkDecoderFunc func(data []byte, sampleRate int) ([]byte, error)

// DecodeSilk 实现 SilkDecoder
func (f SilkDecoderFunc) DecodeSilk(data []byte, sampleRate int) ([]byte, error) {
	return f(data, sampleRate)
}

// GoSilkDecoder 基于 wdvxdr1123/go-silk 的纯 Go 编解码后端（默认）
type GoSilkDecoder struct{}

// DecodeSilk 实现 SilkDecoder
func (GoSilkDecoder) DecodeSilk(data []byte, sampleRate int) ([]byte, error) {
	return gosilk.DecodeSilkBuffToPcm(data, sampleRate)
}

// EncodeSilk 实现 SilkEncoder（bitRate 取库默认 25000，tencent=true 兼容微信语音封装）
func (GoSilkDecoder) EncodeSilk(data []byte, sampleRate int) ([]byte, error) {
	return gosilk.EncodePcmBuffToSilk(data, sampleRate, 25000, true)
}

var silkDecoder SilkDecoder = GoSilkDecoder{}
var silkEncoder SilkEncoder = GoSilkDecoder{}

// SetSilkDecoder 注册全局 SILK 解码器（传 nil 恢复默认 GoSilkDecoder）
func SetSilkDecoder(d SilkDecoder) {
	if d == nil {
		silkDecoder = GoSilkDecoder{}
		return
	}
	silkDecoder = d
}

// SetSilkEncoder 注册全局 SILK 编码器（传 nil 恢复默认）
func SetSilkEncoder(e SilkEncoder) {
	if e == nil {
		silkEncoder = GoSilkDecoder{}
		return
	}
	silkEncoder = e
}

// stripSilkHeader 归一微信语音 STX 头：go-silk 解码器需要保留 0x02 前缀，
// 对缺少该前缀的裸 #!SILK_V3 流补齐，不能误删后交给解码器。
func stripSilkHeader(data []byte) []byte {
	if bytes.HasPrefix(data, []byte{0x02}) && bytes.Contains(data, []byte(silkMagic)) {
		return data
	}
	if bytes.HasPrefix(data, []byte(silkMagic)) {
		return append([]byte{0x02}, data...)
	}
	return data
}

// DetectAudioFormat 魔数探测：RIFF/WAVE → wav，ID3 或 MPEG 帧同步 → mp3，#!SILK_V3 → silk
// （含微信语音 0x02 STX 前缀形态）
func DetectAudioFormat(buf []byte) AudioFormat {
	if len(buf) >= 12 && string(buf[0:4]) == "RIFF" && string(buf[8:12]) == "WAVE" {
		return FormatWAV
	}
	if len(buf) >= 3 && string(buf[0:3]) == "ID3" {
		return FormatMP3
	}
	if len(buf) >= 2 && buf[0] == 0xff && buf[1]&0xe0 == 0xe0 {
		return FormatMP3
	}
	if bytes.Contains(buf, []byte(silkMagic)) {
		return FormatSILK
	}
	return FormatNone
}

// DecodeSilk SILK → PCM s16le（自动去 0x02 STX 头；sampleRate<=0 取 DefaultSilkSampleRate）
func DecodeSilk(data []byte, sampleRate int) ([]byte, error) {
	if sampleRate <= 0 {
		sampleRate = DefaultSilkSampleRate
	}
	pcm, err := silkDecoder.DecodeSilk(stripSilkHeader(data), sampleRate)
	if err != nil {
		return nil, err
	}
	if len(pcm) == 0 {
		return nil, ErrEmptySilkDecode
	}
	return pcm, nil
}

// EncodeSilk PCM s16le → SILK（测试素材生成与互转用；微信口径 24kHz）
func EncodeSilk(pcm []byte, sampleRate int) ([]byte, error) {
	if sampleRate <= 0 {
		sampleRate = DefaultSilkSampleRate
	}
	return silkEncoder.EncodeSilk(pcm, sampleRate)
}

// WrapWAV 把 PCM16LE 单声道封装为 WAV（44 字节标准头，与 Node wrapWav 逐字段一致）
func WrapWAV(pcm []byte, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = DefaultSilkSampleRate
	}
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(pcm)))
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16) // fmt 块长度
	binary.LittleEndian.PutUint16(header[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(header[22:], 1)  // 单声道
	binary.LittleEndian.PutUint32(header[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:], uint32(sampleRate*2)) // 字节率
	binary.LittleEndian.PutUint16(header[32:], 2)                    // 块对齐
	binary.LittleEndian.PutUint16(header[34:], 16)                   // 位深
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))
	return append(header, pcm...)
}

// PCMToWAV PCM s16le 单声道 → WAV（WrapWAV 别名，兼容并行实现命名）
func PCMToWAV(pcm []byte, sampleRate int) []byte { return WrapWAV(pcm, sampleRate) }

// SilkToWAV SILK → WAV（先解码 PCM 再封装；口径同 silk-wasm 解码后封装 WAV）
func SilkToWAV(data []byte, sampleRate int) ([]byte, error) {
	pcm, err := DecodeSilk(data, sampleRate)
	if err != nil {
		return nil, err
	}
	if sampleRate <= 0 {
		sampleRate = DefaultSilkSampleRate
	}
	return WrapWAV(pcm, sampleRate), nil
}

// ParseWAV 校验并拆出 PCM 与采样率（测试与互操作用）
func ParseWAV(buf []byte) (pcm []byte, sampleRate int, err error) {
	if len(buf) < 44 || string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "WAVE" {
		return nil, 0, errors.New("WAV: 头部非法")
	}
	if binary.LittleEndian.Uint16(buf[20:]) != 1 {
		return nil, 0, errors.New("WAV: 仅支持 PCM 格式")
	}
	sampleRate = int(binary.LittleEndian.Uint32(buf[24:]))
	for off := 12; off+8 <= len(buf); {
		id := string(buf[off : off+4])
		size := int(binary.LittleEndian.Uint32(buf[off+4:]))
		if id == "data" {
			end := off + 8 + size
			if end > len(buf) {
				end = len(buf)
			}
			return buf[off+8 : end], sampleRate, nil
		}
		off += 8 + size + size%2
	}
	return nil, 0, errors.New("WAV: 缺少 data 块")
}

// Converted 语音转换结果
type Converted struct {
	// Data 转换后的文件内容
	Data []byte
	// Ext 目标扩展名（wav / mp3）
	Ext string
	// Format 探测出的原始格式
	Format AudioFormat
}

// ConvertVoice 语音下载内容转可发送音频（wav/mp3 直出，silk 解码后封装 WAV），
// 口径对齐 Node convertVoice；SILK 输出采样率固定 DefaultSilkSampleRate
func ConvertVoice(buf []byte) (Converted, error) {
	if len(buf) == 0 {
		return Converted{}, ErrEmptyVoice
	}
	switch DetectAudioFormat(buf) {
	case FormatWAV:
		return Converted{Data: buf, Ext: "wav", Format: FormatWAV}, nil
	case FormatMP3:
		return Converted{Data: buf, Ext: "mp3", Format: FormatMP3}, nil
	case FormatSILK:
		pcm, err := DecodeSilk(buf, DefaultSilkSampleRate)
		if err != nil {
			return Converted{}, err
		}
		return Converted{Data: WrapWAV(pcm, DefaultSilkSampleRate), Ext: "wav", Format: FormatSILK}, nil
	default:
		return Converted{}, fmt.Errorf("%w", ErrUnknownAudio)
	}
}
