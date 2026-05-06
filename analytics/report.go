package analytics

/*

  — Contribution Report API

  HOW IT WORKS:
  Two data sources are merged for a complete picture:

  1. Historical (DB): contributions table — already
     flushed 30s windows, aggregated by user.

  2. Live (RAM): tracker.GetSnapshot() — the current
     30s window not yet written to DB.

  We merge them so the analytics API shows real-time
  data without waiting for the next flush tick.
  The contribution bars in the sidebar update instantly
  as users type, not with a 30-second lag.

*/

// UserStat is the response shape for the contribution report.
type UserStat struct {
	UserID     string  `json:"user_id"`
	Name       string  `json:"name"`
	EditCount  int     `json:"edit_count"`
	BytesAdded int     `json:"bytes_added"`
	ActiveSecs int     `json:"active_secs"`
	Percentage float64 `json:"percentage"` // 0-100
}

// MergeWithLive combines historical DB stats with current in-memory data.
// historicalRows: result of store.GetContributions(docID)
// liveSnapshot:   result of tracker.GetSnapshot(docID)
func MergeWithLive(historicalRows []map[string]any, liveSnapshot map[string]*Activity) []UserStat {
	// Build a map from historical data
	statsMap := make(map[string]*UserStat)

	for _, row := range historicalRows {
		uid, _ := row["user_id"].(string)
		name, _ := row["name"].(string)
		edits, _ := row["total_edits"].(int64)
		bytes, _ := row["total_bytes"].(int64)
		secs, _ := row["total_secs"].(int64)

		statsMap[uid] = &UserStat{
			UserID:     uid,
			Name:       name,
			EditCount:  int(edits),
			BytesAdded: int(bytes),
			ActiveSecs: int(secs),
		}
	}

	// Merge in live (unflushed) data
	for uid, act := range liveSnapshot {
		if s, ok := statsMap[uid]; ok {
			s.EditCount += act.EditCount
			s.BytesAdded += act.BytesAdded
			s.ActiveSecs += act.ActiveSecs
		} else {
			statsMap[uid] = &UserStat{
				UserID:     uid,
				EditCount:  act.EditCount,
				BytesAdded: act.BytesAdded,
				ActiveSecs: act.ActiveSecs,
			}
		}
	}

	// Convert to sorted slice and compute percentages
	var stats []UserStat
	totalBytes := 0
	for _, s := range statsMap {
		totalBytes += s.BytesAdded
	}
	for _, s := range statsMap {
		if totalBytes > 0 {
			s.Percentage = float64(s.BytesAdded) / float64(totalBytes) * 100
		}
		stats = append(stats, *s)
	}

	// Sort by bytes descending (highest contributor first)
	for i := 0; i < len(stats); i++ {
		for j := i + 1; j < len(stats); j++ {
			if stats[j].BytesAdded > stats[i].BytesAdded {
				stats[i], stats[j] = stats[j], stats[i]
			}
		}
	}

	return stats
}
