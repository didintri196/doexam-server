package health

import "context"

// Pinger is a port that checks whether a dependency is reachable.
// Interface ini dimiliki oleh layer service dan ditulis di sini.
// Implementasi konkret (GORM) ada di repo.go, tapi service hanya bergantung pada interface ini.
type Pinger interface {
	Ping(ctx context.Context) error
}
