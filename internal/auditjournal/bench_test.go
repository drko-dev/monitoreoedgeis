package auditjournal

import (
	"strconv"
	"testing"
)

// BenchmarkAppend measures real append cost (write + fsync) at low, medium,
// and high record counts. Security audit events are low-volume by design —
// this exists to catch an accidental full-journal-scan-per-append
// regression, not to hit an invented target.
func BenchmarkAppend(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			dataDir := b.TempDir()
			j, err := Open(dataDir)
			if err != nil {
				b.Fatalf("Open: %v", err)
			}
			defer j.Close()
			b.ResetTimer()
			for i := 0; i < n && i < b.N; i++ {
				if _, err := j.Append(Record{EventType: EventControlCommandReceived, Result: ResultSuccess}); err != nil {
					b.Fatalf("Append: %v", err)
				}
			}
		})
	}
}
