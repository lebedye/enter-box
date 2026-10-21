//go:build !linux

package proxyapi

// Управление сетевой службой предназначено для Linux. Остальные платформы
// могут использовать API-клиент без Linux-блокировки для удалённого сервера.
func (c *Client) LockControl() (func(), error) { return func() {}, nil }
