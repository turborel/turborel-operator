package instance

import (
	"strconv"
	"strings"
)

// replicaStatus distinguishes serving SQL from a group safe to roll. A backup
// must hear its primary. Idle backups do not acknowledge commit heartbeats,
// so the primary checks its outgoing links without requiring recent replies.
func replicaStatus(output string) Status {
	var status Status
	var primaryFresh, allPeersConnected = false, true
	selfRows := 0
	for row := range strings.SplitSeq(output, "\n") {
		fields := strings.Split(row, "|")
		if len(fields) != 6 {
			return Status{}
		}
		status.Members++
		if fields[0] == "YES" {
			selfRows++
			epoch, epochErr := strconv.ParseUint(fields[2], 10, 64)
			view, viewErr := strconv.ParseUint(fields[3], 10, 64)
			if epochErr != nil || viewErr != nil {
				return Status{}
			}
			status.Epoch, status.View = epoch, view
			switch fields[1] {
			case "PRIMARY":
				status.Role = "primary"
			case "BACKUP":
				status.Role = "replica"
			default:
				return Status{}
			}
			continue
		}
		heard, err := strconv.ParseUint(fields[5], 10, 64)
		fresh := fields[4] == "YES" && err == nil && heard <= 5000
		allPeersConnected = allPeersConnected && fields[4] == "YES"
		if fields[1] == "PRIMARY" {
			primaryFresh = fresh
		}
	}
	status.Ready = selfRows == 1 && status.Members > 1
	status.Healthy = status.Ready && ((status.Role == "primary" && allPeersConnected) || (status.Role == "replica" && primaryFresh))
	return status
}
