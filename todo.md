# TODO

## 高優先

- [x] 為 RabbitMQ 狀態流程加入整合測試，涵蓋 API 重啟、暫時斷線、訊息重送、manual ACK、publisher confirm，以及 durable queue 恢復後更新 DB。
- [x] 將 AMQP publisher 整合為可重用 singleton，連線與 channel 改為延遲建立，並補上斷線重連與服務關閉釋放資源的測試。
- [x] 實作 embedding job DB outbox 與 dispatcher 重試。目前 publish 失敗會同步 embedding，但 DB 建立成功後若程序崩潰，仍可能沒有送出 job。
- [x] 將刪除流程改為持久化 saga/cleanup job。現有補償為 best effort，若恢復 storage 或 vector 也失敗，仍需要可重試的清理紀錄與 reconciliation worker。
- [x] 處理同 hash 檔案在 upload/delete 併發時的 reference count 競態，避免判斷為最後一筆後誤刪 physical file。

## 正確性與驗證

- [x] 為 embedding status 加入事件版本或單調狀態轉移規則，避免延遲或重送的 `started` 事件把 `completed`、`failed`、`skipped` 覆蓋回 `processing`。
- [ ] 使用實際 PostgreSQL 驗證權限分頁 SQL、索引與查詢計畫；目前自動化測試只覆蓋 SQLite。
- [ ] 使用實際 Qdrant 與 storage backend 驗證刪除失敗補償。現有測試使用 mock，vector 恢復是重新 embedding，不保證與原始向量完全相同。
- [ ] 補上多 API replica 共用 durable status queue 的測試，確認 competing consumers、冪等更新與 shutdown 行為。

## 維護

- [x] 更新部署設定與文件，加入 `RABBITMQ_EMBEDDING_STATUS_QUEUE` 與 `RABBITMQ_EMBEDDING_STATUS_ROUTING_KEY`。
