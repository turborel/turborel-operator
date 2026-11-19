package instance

import "testing"

func TestReplicaHealthRequiresFreshPrimaryLinks(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, rows     string
		ready, healthy bool
	}{
		{"primary", "YES|PRIMARY|0|4|YES|\nNO|BACKUP|0|4|YES|100\nNO|BACKUP|0|4|YES|200", true, true},
		{"primary missing peer", "YES|PRIMARY|0|4|YES|\nNO|BACKUP|0|4|YES|100\nNO|BACKUP|0|4|NO|6000", true, false},
		{"idle primary", "YES|PRIMARY|0|4|YES|\nNO|BACKUP|0|4|YES|600000\nNO|BACKUP|0|4|YES|600000", true, true},
		{"backup peers need not send heartbeats", "NO|PRIMARY|0|4|YES|100\nYES|BACKUP|0|4|YES|\nNO|BACKUP|0|4|YES|600000", true, true},
		{"backup lost primary", "NO|PRIMARY|0|4|NO|100\nYES|BACKUP|0|4|YES|\nNO|BACKUP|0|4|YES|100", true, false},
		{"unknown view", "YES|UNKNOWN|0||YES|", false, false},
		{"malformed", "not a status", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := replicaStatus(test.rows)
			if got.Ready != test.ready || got.Healthy != test.healthy {
				t.Fatalf("status = %+v", got)
			}
		})
	}
}
