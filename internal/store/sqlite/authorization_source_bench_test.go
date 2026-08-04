package sqlite_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ndzuki/release-manager/internal/store/sqlite"
)

// BenchmarkAuthorizationGuardSerialization measures the single-row
// authorization_source_version guard (UPDATE version = version + 1) under
// concurrent writers. REQ-027 accepts guard-row serialization below 100 TPS;
// the guard must stay far above that ceiling.
func BenchmarkAuthorizationGuardSerialization(b *testing.B) {
	st := sqlite.OpenTest(b)
	db := st.DB()
	ctx := context.Background()

	run := func(b *testing.B, writers int) {
		b.Helper()
		_, err := db.ExecContext(ctx, `UPDATE authorization_source_version SET version = 0 WHERE id = TRUE`)
		if err != nil {
			b.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make(chan error, writers)
		perWriter := b.N / writers
		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for range perWriter {
					result, execErr := db.ExecContext(ctx, `UPDATE authorization_source_version SET version = version + 1 WHERE id = TRUE`)
					if execErr != nil {
						errs <- execErr
						return
					}
					rows, rowsErr := result.RowsAffected()
					if rowsErr != nil {
						errs <- rowsErr
						return
					}
					if rows != 1 {
						errs <- fmt.Errorf("guard row missing")
						return
					}
				}
			}()
		}
		b.ResetTimer()
		started := time.Now()
		close(start)
		wg.Wait()
		elapsed := time.Since(started)
		close(errs)
		for err := range errs {
			b.Fatal(err)
		}
		total := int64(b.N)
		b.ReportMetric(float64(total)/elapsed.Seconds(), "updates/s")
		b.ReportMetric(elapsed.Seconds()/float64(total)*1e6, "us/update")
	}

	for _, writers := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("writers=%d", writers), func(b *testing.B) {
			run(b, writers)
		})
	}
}
