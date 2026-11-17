package service

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestShareService_EncodedAttachmentFallbackUsesDecodedName(t *testing.T) {
	for _, tc := range []struct{ raw, path string }{
		{"Pasted%20image%2020261026235136.png", "Pasted image 20261026235136.png"},
		{"1%201.txt", "1 1.txt"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			repo := new(domainmocks.MockFileRepository)
			file := &domain.File{ID: 42, Path: "attachments/" + tc.path}
			repo.On("GetByPath", mock.Anything, "notes/"+tc.path, int64(3), int64(2)).Return(nil, nil)
			repo.On("GetByPathLike", mock.Anything, mock.Anything, int64(3), int64(2)).Run(func(args mock.Arguments) {
				require.Equal(t, tc.path, args.String(1))
			}).Return(file, nil)
			svc := &shareService{fileRepo: repo}
			refs, err := svc.resolveSharedNoteFiles(context.Background(), 2, 3, "notes/note.md", "![]("+tc.raw+")")
			require.NoError(t, err)
			require.Equal(t, file, refs[tc.raw])
			repo.AssertExpectations(t)
		})
	}
}

func TestRewriteMarkdownImageLinks_TextAttachmentBecomesAccessibleLink(t *testing.T) {
	file := &domain.File{ID: 42, Path: "attachments/1 1.txt"}
	content := rewriteMarkdownImageLinks("![](1%201.txt)", map[string]*domain.File{"1%201.txt": file}, "share-token", "")
	require.Equal(t, `<a href="/api/share/file?id=42&amp;share_token=share-token" target="_blank">attachments/1 1.txt</a>`, content)
	malicious := rewriteMarkdownImageLinks("![<script>alert(1)</script>](1%201.txt)", map[string]*domain.File{"1%201.txt": file}, "share-token", "")
	require.NotContains(t, malicious, "<script>")
	require.Contains(t, malicious, "&lt;script&gt;")
}
