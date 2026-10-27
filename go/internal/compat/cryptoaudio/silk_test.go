package cryptoaudio

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// 本测试覆盖包内 SILK 适配层（silk.go 的可插拔后端实现）：
// WAV 封装/解析、魔数探测、微信 STX 形态、PCM↔SILK 往返、ConvertVoice 口径。
// 全部素材本地生成（正弦 PCM → SILK），不使用外部服务或真实语音数据。

// genSinePCM 生成 16bit 单声道正弦 PCM（测试素材）。
func genSinePCM(sampleRate, ms int, freq float64) []byte {
	n := sampleRate * ms / 1000
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate)))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

func TestWrapWAVAndParseWAV(t *testing.T) {
	pcm := genSinePCM(DefaultSilkSampleRate, 100, 440)
	wav := WrapWAV(pcm, DefaultSilkSampleRate)
	if len(wav) != 44+len(pcm) {
		t.Fatalf("WAV 长度 = %d", len(wav))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatal("RIFF/WAVE 魔数不符")
	}
	if binary.LittleEndian.Uint32(wav[4:8]) != uint32(36+len(pcm)) {
		t.Fatal("RIFF 长度字段不符")
	}
	if binary.LittleEndian.Uint16(wav[20:22]) != 1 || binary.LittleEndian.Uint16(wav[22:24]) != 1 {
		t.Fatal("fmt 非 PCM 单声道")
	}
	if binary.LittleEndian.Uint32(wav[24:28]) != DefaultSilkSampleRate {
		t.Fatal("采样率字段不符")
	}
	if binary.LittleEndian.Uint16(wav[34:36]) != 16 {
		t.Fatal("位深不符")
	}

	// ParseWAV 往返
	gotPCM, rate, err := ParseWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	if rate != DefaultSilkSampleRate || !bytes.Equal(gotPCM, pcm) {
		t.Fatalf("ParseWAV 回读不一致: rate=%d len=%d", rate, len(gotPCM))
	}
	if _, _, err := ParseWAV([]byte("bad")); err == nil {
		t.Fatal("非法 WAV 应报错")
	}
	// PCMToWAV 别名一致
	if !bytes.Equal(PCMToWAV(pcm, DefaultSilkSampleRate), wav) {
		t.Fatal("PCMToWAV 与 WrapWAV 不一致")
	}
}

func TestDetectAudioFormat(t *testing.T) {
	pcm := genSinePCM(DefaultSilkSampleRate, 20, 440)
	if got := DetectAudioFormat(WrapWAV(pcm, DefaultSilkSampleRate)); got != FormatWAV {
		t.Fatalf("wav 探测 = %q", got)
	}
	if got := DetectAudioFormat([]byte("ID3\x03\x00")); got != FormatMP3 {
		t.Fatalf("mp3(ID3) 探测 = %q", got)
	}
	if got := DetectAudioFormat([]byte{0xff, 0xfb, 0x90, 0x00}); got != FormatMP3 {
		t.Fatalf("mp3(帧同步) 探测 = %q", got)
	}
	if got := DetectAudioFormat([]byte("\x02#!SILK_V3")); got != FormatSILK {
		t.Fatalf("silk(含 STX) 探测 = %q", got)
	}
	if got := DetectAudioFormat([]byte("#!SILK_V3")); got != FormatSILK {
		t.Fatalf("silk 探测 = %q", got)
	}
	if got := DetectAudioFormat([]byte("plain")); got != FormatNone {
		t.Fatalf("未知探测 = %q", got)
	}
}

// TestSilkRoundTrip 生成 PCM → 编 SILK（微信变体 tencent 封装）→ 解码回 PCM。
func TestSilkRoundTrip(t *testing.T) {
	pcm := genSinePCM(DefaultSilkSampleRate, 500, 440)
	data, err := EncodeSilk(pcm, DefaultSilkSampleRate)
	if err != nil {
		t.Fatalf("SILK 编码: %v", err)
	}
	if DetectAudioFormat(data) != FormatSILK {
		t.Fatal("编码产物应可探测为 silk")
	}
	decoded, err := DecodeSilk(data, DefaultSilkSampleRate)
	if err != nil {
		t.Fatalf("SILK 解码: %v", err)
	}
	// 有损编码：长度近似即可（±20%）
	lo, hi := len(pcm)*8/10, len(pcm)*12/10
	if len(decoded) < lo || len(decoded) > hi {
		t.Fatalf("解码 PCM 长度 = %d，期望约 %d", len(decoded), len(pcm))
	}

	// SilkToWAV 一体化转换
	wav, err := SilkToWAV(data, 0) // 0 → 默认 24kHz
	if err != nil {
		t.Fatal(err)
	}
	if DetectAudioFormat(wav) != FormatWAV {
		t.Fatal("SilkToWAV 应输出 wav")
	}
	if _, err := DecodeSilk([]byte("not-silk"), 0); err == nil {
		t.Fatal("非法 silk 应报错")
	}
}

// TestSilkDecoderInjection 可插拔后端：注入桩验证 SetSilkDecoder/SetSilkEncoder 生效。
func TestSilkDecoderInjection(t *testing.T) {
	defer SetSilkDecoder(nil)
	defer SetSilkEncoder(nil)

	called := false
	SetSilkDecoder(SilkDecoderFunc(func(data []byte, sampleRate int) ([]byte, error) {
		called = true
		if sampleRate != DefaultSilkSampleRate {
			t.Fatalf("sampleRate = %d", sampleRate)
		}
		return []byte{1, 2, 3, 4}, nil
	}))
	pcm, err := DecodeSilk([]byte("\x02#!SILK_V3 x"), 0)
	if err != nil || !called {
		t.Fatalf("注入解码器未生效: %v", err)
	}
	if !bytes.Equal(pcm, []byte{1, 2, 3, 4}) {
		t.Fatal("桩返回值不一致")
	}

	SetSilkEncoder(stubEncoder{})
	out, err := EncodeSilk([]byte{0}, DefaultSilkSampleRate)
	if err != nil || string(out) != "stub-silk" {
		t.Fatalf("注入编码器未生效: %q %v", out, err)
	}

	SetSilkDecoder(SilkDecoderFunc(func([]byte, int) ([]byte, error) { return nil, nil }))
	if _, err := DecodeSilk([]byte("#!SILK_V3"), 0); err != ErrEmptySilkDecode {
		t.Fatalf("空解码结果应报 ErrEmptySilkDecode, got %v", err)
	}
}

// stubEncoder 测试桩编码器。
type stubEncoder struct{}

func (stubEncoder) EncodeSilk([]byte, int) ([]byte, error) { return []byte("stub-silk"), nil }

func TestConvertVoice(t *testing.T) {
	pcm := genSinePCM(DefaultSilkSampleRate, 300, 440)

	// wav 直出
	wav := WrapWAV(pcm, DefaultSilkSampleRate)
	got, err := ConvertVoice(wav)
	if err != nil || got.Ext != "wav" || got.Format != FormatWAV || !bytes.Equal(got.Data, wav) {
		t.Fatalf("wav 直出失败: %+v %v", got, err)
	}

	// mp3 直出
	mp3 := []byte("ID3\x04\x00\x00\x00\x00\x00\x00")
	got, err = ConvertVoice(mp3)
	if err != nil || got.Ext != "mp3" || got.Format != FormatMP3 || !bytes.Equal(got.Data, mp3) {
		t.Fatalf("mp3 直出失败: %+v %v", got, err)
	}

	// silk → wav
	silkData, err := EncodeSilk(pcm, DefaultSilkSampleRate)
	if err != nil {
		t.Fatal(err)
	}
	got, err = ConvertVoice(silkData)
	if err != nil {
		t.Fatalf("silk 转码: %v", err)
	}
	if got.Ext != "wav" || got.Format != FormatSILK || DetectAudioFormat(got.Data) != FormatWAV {
		t.Fatalf("silk 应转出 wav: %+v", got)
	}

	if _, err := ConvertVoice(nil); err != ErrEmptyVoice {
		t.Fatalf("空输入应报 ErrEmptyVoice, got %v", err)
	}
	if _, err := ConvertVoice([]byte("plain")); err == nil {
		t.Fatal("未知编码应报错")
	}
}
