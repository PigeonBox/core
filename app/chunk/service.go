package chunk

import (
	"context"
	"errors"
	"fmt"

	"github.com/filescodebox/core/pkg/utils"
	"github.com/filescodebox/core/repo/db/dao"
	"github.com/filescodebox/core/repo/db/model"
)

type InitiateUploadReq struct {
	UploadID    string
	FileName    string
	TotalChunks int
	FileSize    int64
	ChunkSize   int
}

type UploadChunkReq struct {
	UploadID   string
	ChunkIndex int
	ChunkHash  string
	ChunkSize  int
}

type ChunkResp struct {
	ID          uint   `json:"id"`
	UploadID    string `json:"upload_id"`
	ChunkIndex  int    `json:"chunk_index"`
	ChunkHash   string `json:"chunk_hash"`
	TotalChunks int    `json:"total_chunks"`
	FileSize    int64  `json:"file_size"`
	ChunkSize   int    `json:"chunk_size"`
	FileName    string `json:"file_name"`
	Completed   bool   `json:"completed"`
	Status      string `json:"status"`
}

type ProgressResp struct {
	UploadID        string  `json:"upload_id"`
	TotalChunks     int     `json:"total_chunks"`
	CompletedChunks int64   `json:"completed_chunks"`
	Progress        float64 `json:"progress"`
	Status          string  `json:"status"`
}

type Service struct {
	chunkRepo    *dao.ChunkRepository
	fileCodeRepo *dao.FileCodeRepository
}

func NewService() *Service {
	return &Service{
		chunkRepo:    dao.NewChunkRepository(),
		fileCodeRepo: dao.NewFileCodeRepository(),
	}
}

// InitiateUpload 初始化分片上传
func (s *Service) InitiateUpload(ctx context.Context, req *InitiateUploadReq) (*ChunkResp, error) {
	// 上传大小 + 类型校验（应用层）
	maxSize := utils.GetMaxUploadSize()
	if err := utils.CheckUploadSize(req.FileSize, maxSize); err != nil {
		return nil, fmt.Errorf("文件过大: 最大允许 %d 字节", maxSize)
	}
	if utils.IsBlockedExtension(req.FileName, utils.DefaultBlockedExtensions()) {
		return nil, fmt.Errorf("该文件类型禁止上传")
	}

	// 检查是否已存在相同的上传ID
	existing, err := s.chunkRepo.GetByUploadID(ctx, req.UploadID)
	if err == nil && existing != nil {
		return nil, errors.New("upload ID already exists")
	}

	// 创建控制记录（chunk_index = -1）
	// ChunkHash 记录客户端提供的整文件哈希（秒传检索用；分片哈希存各分片记录）
	chunk := &model.UploadChunk{
		UploadID:    req.UploadID,
		ChunkIndex:  -1, // 控制记录标识
		TotalChunks: req.TotalChunks,
		FileSize:    req.FileSize,
		ChunkSize:   req.ChunkSize,
		FileName:    utils.SanitizeFileName(req.FileName),
		Status:      "pending",
	}

	err = s.chunkRepo.Create(ctx, chunk)
	if err != nil {
		return nil, err
	}

	return &ChunkResp{
		ID:          chunk.ID,
		UploadID:    chunk.UploadID,
		ChunkIndex:  chunk.ChunkIndex,
		ChunkHash:   chunk.ChunkHash,
		TotalChunks: chunk.TotalChunks,
		FileSize:    chunk.FileSize,
		ChunkSize:   chunk.ChunkSize,
		FileName:    chunk.FileName,
		Completed:   chunk.Completed,
		Status:      chunk.Status,
	}, nil
}

// UploadChunk 上传单个分片
func (s *Service) UploadChunk(ctx context.Context, req *UploadChunkReq) (*ChunkResp, error) {
	// 检查上传ID是否存在
	controlChunk, err := s.chunkRepo.GetByUploadID(ctx, req.UploadID)
	if err != nil {
		return nil, fmt.Errorf("upload ID not found: %v", err)
	}

	// 检查分片索引是否有效
	if req.ChunkIndex < 0 || req.ChunkIndex >= controlChunk.TotalChunks {
		return nil, errors.New("invalid chunk index")
	}

	// 检查分片是否已存在
	existingChunk, err := s.chunkRepo.GetChunkByIndex(ctx, req.UploadID, req.ChunkIndex)
	if err == nil && existingChunk.Completed {
		return &ChunkResp{
			ID:          existingChunk.ID,
			UploadID:    existingChunk.UploadID,
			ChunkIndex:  existingChunk.ChunkIndex,
			ChunkHash:   existingChunk.ChunkHash,
			TotalChunks: controlChunk.TotalChunks,
			FileSize:    controlChunk.FileSize,
			ChunkSize:   existingChunk.ChunkSize,
			FileName:    controlChunk.FileName,
			Completed:   existingChunk.Completed,
			Status:      existingChunk.Status,
		}, nil // 分片已完成，直接返回
	}

	// 创建或更新分片记录
	chunk := &model.UploadChunk{
		UploadID:   req.UploadID,
		ChunkIndex: req.ChunkIndex,
		ChunkHash:  req.ChunkHash,
		ChunkSize:  req.ChunkSize,
		Status:     "completed",
		Completed:  true,
	}

	if existingChunk != nil {
		// 更新现有记录
		err = s.chunkRepo.UpdateChunkCompleted(ctx, req.UploadID, req.ChunkIndex, req.ChunkHash)
		if err != nil {
			return nil, err
		}
		chunk.ID = existingChunk.ID
	} else {
		// 创建新记录
		err = s.chunkRepo.Create(ctx, chunk)
		if err != nil {
			return nil, err
		}
	}

	return &ChunkResp{
		ID:          chunk.ID,
		UploadID:    chunk.UploadID,
		ChunkIndex:  chunk.ChunkIndex,
		ChunkHash:   chunk.ChunkHash,
		TotalChunks: controlChunk.TotalChunks,
		FileSize:    controlChunk.FileSize,
		ChunkSize:   chunk.ChunkSize,
		FileName:    controlChunk.FileName,
		Completed:   chunk.Completed,
		Status:      chunk.Status,
	}, nil
}

// CheckUploadProgress 检查上传进度
func (s *Service) CheckUploadProgress(ctx context.Context, uploadID string) (*ProgressResp, error) {
	controlChunk, err := s.chunkRepo.GetByUploadID(ctx, uploadID)
	if err != nil {
		return nil, err
	}

	completedChunks, err := s.chunkRepo.CountCompletedChunks(ctx, uploadID)
	if err != nil {
		return nil, err
	}

	var progress float64
	if controlChunk.TotalChunks > 0 {
		progress = float64(completedChunks) / float64(controlChunk.TotalChunks) * 100
	}

	return &ProgressResp{
		UploadID:        uploadID,
		TotalChunks:     controlChunk.TotalChunks,
		CompletedChunks: completedChunks,
		Progress:        progress,
		Status:          controlChunk.Status,
	}, nil
}

// CompleteUpload 完成上传
func (s *Service) CompleteUpload(ctx context.Context, uploadID string) error {
	controlChunk, err := s.chunkRepo.GetByUploadID(ctx, uploadID)
	if err != nil {
		return err
	}

	completedChunks, err := s.chunkRepo.CountCompletedChunks(ctx, uploadID)
	if err != nil {
		return err
	}

	if completedChunks < int64(controlChunk.TotalChunks) {
		return errors.New("not all chunks are completed")
	}

	// 更新控制记录状态为已完成
	return s.chunkRepo.UpdateChunkCompleted(ctx, uploadID, -1, "")
}

// GetUploadList 获取上传列表
func (s *Service) GetUploadList(ctx context.Context, page, pageSize int) ([]*ChunkResp, int64, error) {
	chunks, total, err := s.chunkRepo.GetUploadList(ctx, page, pageSize)
	if err != nil {
		return nil, 0, err
	}

	resps := make([]*ChunkResp, len(chunks))
	for i, chunk := range chunks {
		resps[i] = &ChunkResp{
			ID:          chunk.ID,
			UploadID:    chunk.UploadID,
			ChunkIndex:  chunk.ChunkIndex,
			ChunkHash:   chunk.ChunkHash,
			TotalChunks: chunk.TotalChunks,
			FileSize:    chunk.FileSize,
			ChunkSize:   chunk.ChunkSize,
			FileName:    chunk.FileName,
			Completed:   chunk.Completed,
			Status:      chunk.Status,
		}
	}

	return resps, total, nil
}

// DeleteUpload 删除上传
func (s *Service) DeleteUpload(ctx context.Context, uploadID string) error {
	return s.chunkRepo.DeleteByUploadID(ctx, uploadID)
}

// GetUploadedChunkIndexes 获取已上传分片的索引列表
func (s *Service) GetUploadedChunkIndexes(ctx context.Context, uploadID string) ([]int, error) {
	return s.chunkRepo.GetUploadedChunkIndexes(ctx, uploadID)
}

// GetUploadInfo 获取上传信息（包括文件名、大小等）
func (s *Service) GetUploadInfo(ctx context.Context, uploadID string) (*model.UploadChunk, error) {
	return s.chunkRepo.GetByUploadID(ctx, uploadID)
}

// CheckQuickUpload 秒传检查（真实实现，替代原 TODO）：
// 优先按 file_codes.file_hash + size 查既有未过期分享（跨会话秒传），
// 命中直接返回分享码——客户端无需上传任何分片。
func (s *Service) CheckQuickUpload(ctx context.Context, fileHash string, fileSize int64) (string, error) {
	if fileHash == "" || fileSize <= 0 {
		return "", errors.New("invalid file hash")
	}
	fc, err := s.fileCodeRepo.GetByHashAndSize(ctx, fileHash, fileSize)
	if err != nil || fc == nil {
		return "", errors.New("share code not found")
	}
	if fc.IsExpired() {
		return "", errors.New("share expired")
	}
	return fc.Code, nil
}
