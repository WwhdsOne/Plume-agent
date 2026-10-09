//go:build !unix

package soul

import "os"

// openSoulFile 在其他平台保持标准文件打开行为；Read 仍核对实际文件类型。
func openSoulFile(path string) (*os.File, error) {
	return os.Open(path)
}
