//go:build !linux

package main

import (
	"image"

	"fyne.io/fyne/v2"
)

func captureFrame(c fyne.Canvas) image.Image {
	return c.Capture()
}
