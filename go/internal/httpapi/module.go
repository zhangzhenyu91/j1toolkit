package httpapi

import "github.com/go-chi/chi/v5"

// Module 是可插拔业务模块。各应用在自己的目录实现 RegisterRoutes，主程序统一装配。
type Module interface {
	RegisterRoutes(r chi.Router)
}
