// storage.go — 分片物理操作的存储接缝（chunk 域）。
//
// 写片/合并/取流/清理收口于 app 层，HTTP 适配层不直连 storage；
// 磁盘布局（uploads/YYYY/MM/DD/<uuid><ext>）统一走 utils.UploadObjectRelPath，
// 与直传分享落盘同源。未注入统一存储实例时（测试/降级路径）懒加载本地后端
// 兜底，参数与历史 handler 兜底一致（./data）。
package chunk

import (
	"context"
	"io"

	"github.com/pigeonbox/core/pkg/utils"
	"github.com/pigeonbox/core/storage"
)

// SetStorage 注入统一存储实例（bootstrap 调用）。回归要点：chunk 与 share
// 必须共用同一实例——此前两个懒加载单例路径基不一致（./data 与 ./data/uploads），
// 分片合并写入 data/uploads/<rel>、下载却找 data/uploads/uploads/<rel>。
func (s *Service) SetStorage(st storage.StorageInterface) { s.storage = st }

// SaveChunkData 写入单个分片。
func (s *Service) SaveChunkData(ctx context.Context, uploadID string, chunkIndex int, data []byte) error {
	return s.storageClient().SaveChunk(ctx, uploadID, chunkIndex, data)
}

// MergeUpload 合并全部分片为正式上传对象，返回统一布局下的相对路径。
// 文件名仅用于扩展名推导，磁盘名由 UploadObjectRelPath 生成（UUID）。
func (s *Service) MergeUpload(ctx context.Context, uploadID string, totalChunks int, fileName string) (string, error) {
	relPath := utils.UploadObjectRelPath(fileName)
	if err := s.storageClient().MergeChunks(ctx, uploadID, totalChunks, relPath); err != nil {
		return "", err
	}
	return relPath, nil
}

// OpenMerged 打开合并后的对象读取流（哈希计算/魔数复检用），返回读取器与实际大小。
func (s *Service) OpenMerged(ctx context.Context, relPath string) (io.ReadCloser, int64, error) {
	return s.storageClient().GetFileReader(ctx, relPath)
}

// DeleteMergedFile 尽力删除合并对象（校验失败清理路径；删除失败不阻断主流程，
// 与历史 `_ = DeleteFile` 语义一致）。
func (s *Service) DeleteMergedFile(ctx context.Context, relPath string) {
	_ = s.storageClient().DeleteFile(ctx, relPath)
}

// CleanSessionFiles 清理上传会话的全部临时分片。
func (s *Service) CleanSessionFiles(ctx context.Context, uploadID string) error {
	return s.storageClient().CleanChunks(ctx, uploadID)
}

func (s *Service) storageClient() storage.StorageInterface {
	if s.storage != nil {
		return s.storage
	}
	s.fallbackOnce.Do(func() {
		s.fallbackStorage = storage.NewStorageService(&storage.StorageConfig{
			Type:     storage.StorageTypeLocal,
			DataPath: "./data",
			BaseURL:  "http://localhost:12345",
		})
	})
	return s.fallbackStorage
}
