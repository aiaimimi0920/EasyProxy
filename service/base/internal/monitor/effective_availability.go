package monitor

import "time"

const trafficProvenSuccessWindow = 30 * time.Minute

func isProbeAvailable(snap Snapshot) bool {
	return snap.InitialCheckDone && snap.Available && !snap.Blacklisted
}

func isTrafficProvenUsable(snap Snapshot) bool {
	return isTrafficProvenUsableAt(snap, time.Now())
}

func isTrafficProvenUsableAt(snap Snapshot, now time.Time) bool {
	if snap.Blacklisted || snap.LastTrafficSuccessAt.IsZero() {
		return false
	}
	if snap.FailureSeq > 0 && snap.TrafficSuccessSeq > 0 {
		if snap.FailureSeq > snap.TrafficSuccessSeq {
			return false
		}
	} else if !snap.LastFailure.IsZero() && !snap.LastFailure.Before(snap.LastTrafficSuccessAt) {
		return false
	}
	if now.Before(snap.LastTrafficSuccessAt) {
		return true
	}
	return now.Sub(snap.LastTrafficSuccessAt) <= trafficProvenSuccessWindow
}

func effectiveAvailabilityDetails(snap Snapshot) (effective bool, trafficProven bool, source string) {
	return effectiveAvailabilityDetailsAt(snap, time.Now())
}

func effectiveAvailabilityDetailsAt(snap Snapshot, now time.Time) (effective bool, trafficProven bool, source string) {
	probeAvailable := isProbeAvailable(snap)
	trafficProven = isTrafficProvenUsableAt(snap, now)

	switch {
	case probeAvailable && trafficProven:
		return true, true, "probe+recent_traffic"
	case probeAvailable:
		return true, false, "probe"
	case trafficProven:
		return true, true, "recent_traffic"
	default:
		return false, false, ""
	}
}
