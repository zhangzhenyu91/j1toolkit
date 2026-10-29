//go:build ((!gles && !arm && !arm64) || darwin) && !wasm && !test_web_driver

package main

// 与 Fyne v2.8.2 桌面 GL 构建约束一致；这是编译期 API，不是实际 GPU 型号。
const fyneGLBuild = "gl2.1"
