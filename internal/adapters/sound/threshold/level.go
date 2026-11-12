package threshold

import (
	"encoding/binary"
	"math"

	"github.com/maikomr/watchmydog/internal/domain"
)

// rmsDBFS returns the RMS level of 16-bit little-endian mono PCM in dB
// relative to full scale: a full-scale square wave is 0, half the amplitude
// is about -6, and digital silence is domain.SilenceDBFS.
//
// RMS (root mean square) is the usual measure of loudness over a short
// window: it reflects the energy of the sound, where a single peak sample
// would overreact to one click.
func rmsDBFS(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return domain.SilenceDBFS
	}
	var sum float64
	for i := 0; i < n; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
		sum += s * s
	}
	rms := math.Sqrt(sum / float64(n))
	if rms < 1 {
		return domain.SilenceDBFS
	}
	return max(20*math.Log10(rms/32768), domain.SilenceDBFS)
}
