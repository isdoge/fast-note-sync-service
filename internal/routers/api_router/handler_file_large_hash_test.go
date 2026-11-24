package api_router

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The plugin's hashArrayBuffer output for this deterministic 12MB payload, produced by
// a byte-for-byte Node.js reference of src/lib/utils/helpers.ts (head + middle + tail).
// 该确定性 12MB 载荷在插件 hashArrayBuffer 下的输出，由逐字节复刻
// src/lib/utils/helpers.ts 的 Node.js 参考实现算出（前 + 中 + 后 5MB）。
const plugin12MBHash = "1672310686"

// largeUploadPayload reproduces the deterministic payload used by the plugin reference.
func largeUploadPayload(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*31 + 17) & 0xff)
	}
	data[0] = 0xff
	data[1] = 0x80
	data[size-1] = 0xff
	data[size-2] = 0x80
	// Mirror computeMidSliceStart for 12MB: 12MB/2 - 5MB/2 = 3670016.
	midStart := size/2 - 5*1024*1024/2
	if midStart+3 < size {
		data[midStart+1] = 0xff
		data[midStart+2] = 0x80
	}
	return data
}

// TestFileHandler_Upload_LargeFileHashMatchesPlugin verifies that a REST upload above
// the 10MB threshold stores exactly the content hash the plugin computes locally, so
// the plugin's next scan does not see a phantom change and re-upload the file.
// TestFileHandler_Upload_LargeFileHashMatchesPlugin 验证 >10MB 的 REST 上传落库的
// contentHash 与插件本地计算结果完全一致，避免插件下次扫描误判变更而重复上传。
func TestFileHandler_Upload_LargeFileHashMatchesPlugin(t *testing.T) {
	t.Chdir(t.TempDir())
	mockFileSvc := new(svcmocks.MockFileService)

	var writeParams *dto.FileUpdateRequest
	mockFileSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileUpdateRequest"), false).
		Run(func(args mock.Arguments) {
			writeParams = args.Get(2).(*dto.FileUpdateRequest)
		}).
		Return(true, &dto.FileDTO{ID: 1, Path: "big.bin"}, nil)

	const size = 12 * 1024 * 1024
	payload := largeUploadPayload(size)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	require.NoError(t, writer.WriteField("vault", "main"))
	require.NoError(t, writer.WriteField("path", "big.bin"))
	require.NoError(t, writer.WriteField("ctime", "1700000000"))
	require.NoError(t, writer.WriteField("mtime", "1700000000"))
	part, err := writer.CreateFormFile("file", "big.bin")
	require.NoError(t, err)
	_, err = part.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	handler := newTestFileHandler(mockFileSvc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/file/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Client", "Web")
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_token", &pkgapp.UserEntity{UID: 1})

	handler.Upload(c)

	assert.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, writeParams)
	assert.Equal(t, plugin12MBHash, writeParams.ContentHash,
		"REST 上传 >10MB 的落库哈希必须与插件三分段口径一致")
	assert.Equal(t, int64(size), writeParams.Size)
}
