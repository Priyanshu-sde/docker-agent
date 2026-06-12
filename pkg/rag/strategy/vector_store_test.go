package strategy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/model/provider/base"
	"github.com/docker/docker-agent/pkg/modelerrors"
	"github.com/docker/docker-agent/pkg/modelsdev"
	"github.com/docker/docker-agent/pkg/rag/chunk"
	"github.com/docker/docker-agent/pkg/rag/database"
	"github.com/docker/docker-agent/pkg/rag/embed"
	"github.com/docker/docker-agent/pkg/tools"
)

func TestClassifyModelCallError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantAborted bool
	}{
		{name: "nil", err: nil, wantAborted: false},
		{name: "404 model not found", err: &modelerrors.StatusError{StatusCode: http.StatusNotFound, Err: errors.New("not_found_error: model: claude-sonnet-4-7")}, wantAborted: true},
		{name: "401 unauthorized", err: &modelerrors.StatusError{StatusCode: http.StatusUnauthorized, Err: errors.New("unauthorized")}, wantAborted: true},
		{name: "429 rate limited", err: &modelerrors.StatusError{StatusCode: http.StatusTooManyRequests, Err: errors.New("too many requests")}, wantAborted: true},
		{name: "500 server error", err: &modelerrors.StatusError{StatusCode: http.StatusInternalServerError, Err: errors.New("server error")}, wantAborted: false},
		{name: "timeout message", err: errors.New("request timeout"), wantAborted: false},
		{name: "context canceled", err: context.Canceled, wantAborted: false},
		{name: "context deadline", err: context.DeadlineExceeded, wantAborted: false},
		{name: "wrapped 404", err: fmt.Errorf("batch 1 failed: %w", &modelerrors.StatusError{StatusCode: http.StatusNotFound, Err: errors.New("no such model")}), wantAborted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyModelCallError(tt.err)
			assert.Equal(t, tt.wantAborted, isIndexingAborted(got))
			if tt.err != nil {
				assert.ErrorIs(t, got, tt.err)
			}
		})
	}
}

// fakeEmbeddingProvider counts embedding calls and always fails with a fixed error.
type fakeEmbeddingProvider struct {
	calls atomic.Int64
	err   error
}

func (f *fakeEmbeddingProvider) ID() modelsdev.ID        { return modelsdev.NewID("test", "fake-embed") }
func (f *fakeEmbeddingProvider) BaseConfig() base.Config { return base.Config{} }

func (f *fakeEmbeddingProvider) CreateChatCompletionStream(context.Context, []chat.Message, []tools.Tool) (chat.MessageStream, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeEmbeddingProvider) CreateEmbedding(context.Context, string) (*base.EmbeddingResult, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return &base.EmbeddingResult{Embedding: []float64{0.1, 0.2}, TotalTokens: 1}, nil
}

// fakeVectorDB is an in-memory vectorStoreDB used to drive indexing in tests.
type fakeVectorDB struct {
	mu       sync.Mutex
	metadata map[string]database.FileMetadata
}

func newFakeVectorDB() *fakeVectorDB {
	return &fakeVectorDB{metadata: make(map[string]database.FileMetadata)}
}

func (db *fakeVectorDB) AddDocumentWithEmbedding(context.Context, database.Document, []float64, string) error {
	return nil
}

func (db *fakeVectorDB) SearchSimilarVectors(context.Context, []float64, int) ([]VectorSearchResultData, error) {
	return nil, nil
}

func (db *fakeVectorDB) DeleteDocumentsByPath(context.Context, string) error { return nil }

func (db *fakeVectorDB) GetFileMetadata(_ context.Context, sourcePath string) (*database.FileMetadata, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if meta, ok := db.metadata[sourcePath]; ok {
		return &meta, nil
	}
	return nil, nil
}

func (db *fakeVectorDB) SetFileMetadata(_ context.Context, meta database.FileMetadata) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.metadata[meta.SourcePath] = meta
	return nil
}

func (db *fakeVectorDB) GetAllFileMetadata(context.Context) ([]database.FileMetadata, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	all := make([]database.FileMetadata, 0, len(db.metadata))
	for _, meta := range db.metadata {
		all = append(all, meta)
	}
	return all, nil
}

func (db *fakeVectorDB) DeleteFileMetadata(_ context.Context, sourcePath string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	delete(db.metadata, sourcePath)
	return nil
}

func (db *fakeVectorDB) Close() error { return nil }

func newTestVectorStore(t *testing.T, embedErr error) (*VectorStore, *fakeEmbeddingProvider, []string) {
	t.Helper()

	dir := t.TempDir()
	const fileCount = 5
	docPaths := make([]string, 0, fileCount)
	for i := range fileCount {
		path := filepath.Join(dir, fmt.Sprintf("doc%d.txt", i))
		require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, "document %d content", i), 0o644))
		docPaths = append(docPaths, path)
	}

	fake := &fakeEmbeddingProvider{err: embedErr}
	store := NewVectorStore(VectorStoreConfig{
		Name:                 "test",
		Database:             newFakeVectorDB(),
		Embedder:             embed.New(fake),
		EmbeddingConcurrency: 1,
		FileIndexConcurrency: 1,
		Chunking:             ChunkingConfig{Size: 1024, Overlap: 0},
	})

	return store, fake, docPaths
}

func TestInitializeAbortsOnNonRetryableModelError(t *testing.T) {
	embedErr := &modelerrors.StatusError{
		StatusCode: http.StatusNotFound,
		Err:        errors.New("not_found_error: model: claude-sonnet-4-7"),
	}
	store, fake, docPaths := newTestVectorStore(t, embedErr)

	err := store.Initialize(t.Context(), docPaths, ChunkingConfig{Size: 1024})
	require.Error(t, err)
	assert.True(t, isIndexingAborted(err), "error should carry the abort marker")
	assert.Equal(t, int64(1), fake.calls.Load(),
		"indexing must stop after the first non-retryable model error instead of trying every file")
}

func TestInitializeContinuesOnTransientModelError(t *testing.T) {
	embedErr := &modelerrors.StatusError{
		StatusCode: http.StatusInternalServerError,
		Err:        errors.New("internal server error"),
	}
	store, fake, docPaths := newTestVectorStore(t, embedErr)

	err := store.Initialize(t.Context(), docPaths, ChunkingConfig{Size: 1024})
	require.NoError(t, err, "transient errors skip the file and keep indexing")
	assert.Equal(t, int64(len(docPaths)), fake.calls.Load(),
		"every file should still be attempted on transient errors")
}

func TestCheckAndReindexAbortsOnNonRetryableModelError(t *testing.T) {
	embedErr := &modelerrors.StatusError{
		StatusCode: http.StatusTooManyRequests,
		Err:        errors.New("too many requests"),
	}
	store, fake, docPaths := newTestVectorStore(t, embedErr)

	err := store.CheckAndReindexChangedFiles(t.Context(), docPaths, ChunkingConfig{Size: 1024})
	require.Error(t, err)
	assert.True(t, isIndexingAborted(err))
	assert.Equal(t, int64(1), fake.calls.Load())
}

// abortingInputBuilder simulates a semantic-embeddings chat model that fails
// permanently (e.g. invalid chat_model name).
type abortingInputBuilder struct {
	calls atomic.Int64
	err   error
}

func (b *abortingInputBuilder) BuildEmbeddingInput(context.Context, string, chunk.Chunk) (string, error) {
	b.calls.Add(1)
	return "", b.err
}

func TestBuildEmbeddingInputsAbortsOnNonRetryableModelError(t *testing.T) {
	store, fake, docPaths := newTestVectorStore(t, nil)
	builder := &abortingInputBuilder{err: &modelerrors.StatusError{
		StatusCode: http.StatusNotFound,
		Err:        errors.New("not_found_error: model: claude-sonnet-4-7"),
	}}
	store.SetEmbeddingInputBuilder(builder)

	err := store.Initialize(t.Context(), docPaths, ChunkingConfig{Size: 1024})
	require.Error(t, err)
	assert.True(t, isIndexingAborted(err), "permanent LLM errors must abort instead of falling back per chunk")
	assert.Equal(t, int64(0), fake.calls.Load(), "no embedding requests should be sent after the abort")
	assert.Equal(t, int64(1), builder.calls.Load())
}

func TestBuildEmbeddingInputsFallsBackOnTransientError(t *testing.T) {
	store, fake, docPaths := newTestVectorStore(t, nil)
	builder := &abortingInputBuilder{err: errors.New("request timeout")}
	store.SetEmbeddingInputBuilder(builder)

	err := store.Initialize(t.Context(), docPaths, ChunkingConfig{Size: 1024})
	require.NoError(t, err, "transient LLM errors keep the raw-content fallback behavior")
	assert.Equal(t, int64(len(docPaths)), fake.calls.Load())
}
