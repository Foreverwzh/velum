//go:build !webui

package webui

import (
	"io/fs"
	"testing/fstest"
)

// Assets 在没有内嵌界面的构建里返回说明页，发布版本都带 webui 标签。
func Assets() fs.FS {
	return fstest.MapFS{"index.html": {Data: []byte("<!doctype html><meta charset=utf-8><title>Velum</title><p>This build has no web UI. Build with: sh core/scripts/webui.sh and -tags webui</p>\n")}}
}
