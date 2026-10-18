package httpapi

import (
	"fmt"
	"net/http"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.Pool.Query(r.Context(), "SELECT status,count(*) FROM transactions GROUP BY status")
	if err != nil {
		fail(w, err)
		return
	}
	counts := map[string]int64{}
	for rows.Next() {
		var status string
		var n int64
		if err = rows.Scan(&status, &n); err != nil {
			rows.Close()
			fail(w, err)
			return
		}
		counts[status] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		fail(w, err)
		return
	}
	var pending, lag int64
	err = s.Store.Pool.QueryRow(r.Context(), "SELECT count(*),COALESCE(EXTRACT(EPOCH FROM now()-min(created_at))::bigint,0) FROM outbox WHERE published_at IS NULL").Scan(&pending, &lag)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "wallet_sql_concurrency_conflicts_total %d\nwallet_sql_retries_total %d\nwallet_sqs_replays_total %d\nwallet_sqs_duration_seconds_sum %.6f\nwallet_sqs_duration_seconds_count %d\n", s.Store.ConcurrencyConflicts.Load(), s.Store.SQLRetries.Load(), s.Consumer.Replays.Load(), float64(s.Consumer.DurationMicros.Load())/1e6, s.Consumer.Completed.Load())
	for _, status := range []string{"PROCESSED", "REJECTED", "PENDING_REFERENCE", "FAILED"} {
		fmt.Fprintf(w, "wallet_transactions{status=%q} %d\n", status, counts[status])
	}
	fmt.Fprintf(w, "wallet_outbox_pending %d\nwallet_outbox_oldest_age_seconds %d\n", pending, lag)
	fmt.Fprintf(w, "wallet_http_requests_total %d\nwallet_reconciliation_divergences_total %d\nwallet_http_conflicts_total %d\nwallet_http_replays_total %d\nwallet_http_duration_seconds_sum %.6f\nwallet_http_duration_seconds_count %d\n", s.Requests.Load(), s.Divergences.Load(), s.Conflicts.Load(), s.Replays.Load(), float64(s.DurationMicros.Load())/1e6, s.Requests.Load())
	fmt.Fprintf(w, "wallet_sqs_received_total %d\nwallet_sqs_retries_total %d\nwallet_sqs_dlq_total %d\nwallet_outbox_published_total %d\nwallet_outbox_retries_total %d\n", s.Consumer.Received.Load(), s.Consumer.Retries.Load(), s.Consumer.DeadLetters.Load(), s.Publisher.Published.Load(), s.Publisher.Retries.Load())
}
