package speech

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// PCM is mono 16-bit audio at a sample rate.
type PCM struct {
	Rate    int
	Samples []int16
}

// Duration is the length in seconds.
func (p PCM) Duration() float64 {
	if p.Rate <= 0 {
		return 0
	}
	return float64(len(p.Samples)) / float64(p.Rate)
}

// ErrNotWAV means the bytes are not a WAV file this decoder reads (RIFF/WAVE with PCM 8/16/24/32-bit or 32-bit
// float samples).
var ErrNotWAV = errors.New("not a PCM WAV file")

// DecodeWAV reads a RIFF/WAVE file (PCM 8/16/24/32-bit integer or 32/64-bit float, any channel count) and mixes it
// down to mono 16-bit. A data chunk whose size runs past the end (streamed WAV) is read to the end.
func DecodeWAV(b []byte) (PCM, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return PCM{}, ErrNotWAV
	}
	var format, channels, bits uint16
	var rate uint32
	var data []byte
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		end := body + size
		// A streamed WAV (ffmpeg to a pipe) leaves the sizes at 0 or 0xFFFFFFFF: its data runs to the end.
		if end > len(b) || end < body || (id == "data" && size == 0) {
			end = len(b)
		}
		switch id {
		case "fmt ":
			if end-body < 16 {
				return PCM{}, ErrNotWAV
			}
			format = binary.LittleEndian.Uint16(b[body:])
			channels = binary.LittleEndian.Uint16(b[body+2:])
			rate = binary.LittleEndian.Uint32(b[body+4:])
			bits = binary.LittleEndian.Uint16(b[body+14:])
			if format == 0xFFFE && end-body >= 26 { // WAVE_FORMAT_EXTENSIBLE: the sub-format's first two bytes
				format = binary.LittleEndian.Uint16(b[body+24:])
			}
		case "data":
			data = b[body:end]
		}
		off = end + size%2
		if data != nil && format != 0 {
			break
		}
	}
	if format == 0 || data == nil || channels == 0 || rate == 0 {
		return PCM{}, ErrNotWAV
	}
	if format != 1 && format != 3 {
		return PCM{}, fmt.Errorf("%w (format %d)", ErrNotWAV, format)
	}
	width := int(bits) / 8
	if width == 0 || (format == 3 && width != 4 && width != 8) || width > 8 {
		return PCM{}, fmt.Errorf("%w (%d-bit samples)", ErrNotWAV, bits)
	}
	frame := width * int(channels)
	n := len(data) / frame
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		var sum float64
		for c := 0; c < int(channels); c++ {
			sum += sampleAt(data[i*frame+c*width:], width, format == 3)
		}
		out[i] = toInt16(sum / float64(channels))
	}
	return PCM{Rate: int(rate), Samples: out}, nil
}

// sampleAt returns one sample in [-1, 1].
func sampleAt(b []byte, width int, float bool) float64 {
	if float {
		if width == 8 {
			return math.Float64frombits(binary.LittleEndian.Uint64(b))
		}
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
	}
	switch width {
	case 1:
		return (float64(b[0]) - 128) / 128
	case 2:
		return float64(int16(binary.LittleEndian.Uint16(b))) / 32768
	case 3:
		v := int32(b[0]) | int32(b[1])<<8 | int32(int8(b[2]))<<16
		return float64(v) / 8388608
	case 4:
		return float64(int32(binary.LittleEndian.Uint32(b))) / 2147483648
	}
	return 0
}

func toInt16(v float64) int16 {
	v *= 32767
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(math.Round(v))
}

// Resample changes the sample rate (linear interpolation; enough for speech recognition).
func (p PCM) Resample(rate int) PCM {
	if p.Rate == rate || p.Rate <= 0 || len(p.Samples) == 0 {
		return PCM{Rate: rate, Samples: p.Samples}
	}
	n := int(int64(len(p.Samples)) * int64(rate) / int64(p.Rate))
	out := make([]int16, n)
	step := float64(p.Rate) / float64(rate)
	for i := range out {
		pos := float64(i) * step
		j := int(pos)
		frac := pos - float64(j)
		a := float64(p.Samples[min(j, len(p.Samples)-1)])
		b := float64(p.Samples[min(j+1, len(p.Samples)-1)])
		out[i] = int16(math.Round(a + (b-a)*frac))
	}
	return PCM{Rate: rate, Samples: out}
}

// EncodeWAV writes mono 16-bit PCM as a WAV file.
func EncodeWAV(p PCM) []byte {
	var buf bytes.Buffer
	buf.Grow(44 + 2*len(p.Samples))
	buf.Write(WAVHeader(2*len(p.Samples), p.Rate, 1))
	_ = binary.Write(&buf, binary.LittleEndian, p.Samples)
	return buf.Bytes()
}

// WAVHeader is the 44-byte header of a 16-bit PCM WAV file with dataBytes of samples.
func WAVHeader(dataBytes, rate, channels int) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataBytes))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(dataBytes))
	return h
}
