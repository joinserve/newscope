# ADR 0013 實作說明

交給實作者的開發描述。設計決定已經寫在 `docs/adr/0013-beat-groupings.md`，這裡只講怎麼落地。建議拆成 4 個 PR，對應 ADR 的 Phase A–D。

## 必讀先修

- `docs/adr/0013-beat-groupings.md`（7 項決定）
- `docs/adr/0010-cross-item-deduplication-and-merge.md`（beats backend、worker pattern、feature gate 慣例）
- `docs/adr/0011-beats-ui-design.md`（beats UI baseline；本次只動 header）
- `pkg/scheduler/scheduler.go:206`（既有 worker 啟動模式，新 worker 仿這個寫）
- `pkg/scheduler/embed_worker.go`（最接近 entity_worker 的範本；同樣是「ticker + 撈 pending + 呼叫 LLM + 寫回」結構）
- `pkg/repository/beat.go:625`（ListBeats，要在這裡加 LEFT JOIN）
- `pkg/features/`（feature gate 寫法，新增 `EntitiesEnabled`）

## PR 1 — Schema + Grouping CRUD（Phase A）

**目標：** 表格、CRUD API、Settings 頁有可用的管理介面。所有 beat 的 `assignment.grouping_id` 永遠是 NULL，所以 `/beats` 的行為不會改。

### 1.1 Schema

`pkg/repository/schema.sql` 新增：

```sql
CREATE TABLE IF NOT EXISTS groupings (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    slug          TEXT    NOT NULL UNIQUE,
    tags          JSON    NOT NULL DEFAULT '[]',
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_groupings_order ON groupings(display_order);

CREATE TABLE IF NOT EXISTS beat_grouping_assignments (
    beat_id      INTEGER PRIMARY KEY REFERENCES beats(id) ON DELETE CASCADE,
    grouping_id  INTEGER REFERENCES groupings(id) ON DELETE SET NULL,
    computed_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_assignments_grouping
    ON beat_grouping_assignments(grouping_id);
```

`items.entities` / `entities_extracted_at` 留到 PR 3 再加。

### 1.2 Domain

`pkg/domain/grouping.go`（新檔）：

```go
type Grouping struct {
    ID           int64
    Name         string
    Slug         string
    Tags         []string  // lowercase
    DisplayOrder int
    CreatedAt    time.Time
    UpdatedAt    time.Time
}
```

Slug 規則：lowercase, hyphen-separated, ASCII-only。後端 `slugify(name)`（簡單版：lower + 非英數轉 `-` + trim）。撞 unique 時 append `-2`、`-3`。

### 1.3 Repository

`pkg/repository/grouping.go`（新檔）：

- `ListGroupings(ctx) ([]Grouping, error)` — `ORDER BY display_order ASC, id ASC`
- `GetGrouping(ctx, id) (Grouping, error)`
- `GetGroupingBySlug(ctx, slug) (Grouping, error)`
- `CreateGrouping(ctx, g) (id, error)` — `display_order = COALESCE(MAX(display_order), -1) + 1`
- `UpdateGrouping(ctx, g) error` — name + tags + slug
- `DeleteGrouping(ctx, id) error`
- `ReorderGroupings(ctx, idsInOrder []int64) error` — 一個 transaction，按陣列順序設 `display_order = i`

Tags 進 DB 前統一 lowercase + dedup + sort（normalize once，比對省事）。

### 1.4 Routes + handlers

`server/server.go` 加：

```go
r.HandleFunc("GET /settings/groupings", s.groupingsSettingsHandler)
r.HandleFunc("POST /api/v1/groupings", s.createGroupingHandler)
r.HandleFunc("PUT /api/v1/groupings/{id}", s.updateGroupingHandler)
r.HandleFunc("DELETE /api/v1/groupings/{id}", s.deleteGroupingHandler)
r.HandleFunc("POST /api/v1/groupings/reorder", s.reorderGroupingsHandler)
```

CRUD handlers 走 HTMX：表單 submit → repo 寫入 → 回傳 `groupings-list.html` partial swap 整個列表。Reorder 用 SortableJS（少量 vendor JS 可接受）或先做粗暴的「上移/下移」按鈕；ADR 沒指定 UX，先以「上移/下移」最快上線。

### 1.5 Templates

新增：
- `server/templates/groupings.html`（Settings 頁的 section 或獨立頁，看現有 settings.html 結構決定）
- `server/templates/grouping-row.html`（單列：name + tag chips + edit/delete）
- `server/templates/grouping-form.html`（新增/編輯表單）

Tag input：先做最簡 `<input>` 接逗號分隔字串，server 端 split + normalize。Tag autocomplete 留到 Phase D。

### 1.6 Tests

- `Repository.CreateGrouping` / `Update` / `Delete` / `Reorder` table-driven
- Slug collision 測試（重複 name 自動 `-2`）
- Handler 測試：POST 帶逗號分隔 tags → DB 拿出來是排序好的 lowercase array

### 1.7 不要做

- 不要在這 PR 動 `ListBeats`（沒人會被 assignment.grouping_id 影響）
- 不要在這 PR 加 `/beats` header dropdown（assignment 還沒寫，會空空的）
- 不要寫 entity_worker

---

## PR 2 — Assignment engine + ListBeats 整合（Phase B）

**目標：** 寫好 first-match-wins 引擎，beat_worker 在 attach 之後呼叫；grouping CRUD 觸發全量重算；`/beats` 可以用 `?group=<slug>` 過濾。

### 2.1 Engine

`pkg/grouping/grouping.go`（新 package）：

```go
type Engine struct {
    store Store           // GroupingStore + BeatStore subset
    log   logger          // 走專案的 lgr
}

// Reassign 重算單一 beat 的 grouping_id。
// 步驟：
//   1. 撈 beat 的 tag_set（union members.topics）
//   2. 撈所有 groupings ORDER BY display_order
//   3. 找第一個 grouping.tags ⊆ tag_set，命中即 break
//   4. UpsertAssignment(beatID, &groupingID) 或 (beatID, nil)
func (e *Engine) Reassign(ctx, beatID int64) error

// ReassignAll 全量重算，限定 first_seen_at > now - window。
func (e *Engine) ReassignAll(ctx, window time.Duration) error
```

關鍵：tag_set 要 lowercase 比對。subset 判斷：

```go
func subset(required, beatTags []string) bool {
    set := make(map[string]struct{}, len(beatTags))
    for _, t := range beatTags {
        set[strings.ToLower(t)] = struct{}{}
    }
    for _, t := range required {
        if _, ok := set[strings.ToLower(t)]; !ok {
            return false
        }
    }
    return true
}
```

Grouping list 在 `Engine` 裡可以 cache（短 TTL，例如 30 秒），CRUD handler 主動 invalidate。

### 2.2 Repository 補

`pkg/repository/grouping.go`：

- `BeatTagSet(ctx, beatID) ([]string, error)` — 拉 `items.topics` JSON union（PR 3 之後同時 union `entities`，先寫成 only topics，加註解標 PR 3 補）
- `UpsertAssignment(ctx, beatID, groupingID *int64) error` — `INSERT ... ON CONFLICT(beat_id) DO UPDATE`
- `ActiveBeatIDs(ctx, since time.Time) ([]int64, error)` — for `ReassignAll`

### 2.3 接 beat_worker

`pkg/scheduler/beat_worker.go` 在 `AttachOrSeed` 成功後：

```go
beatID, err := s.store.AttachOrSeed(ctx, itemID, neighbor)
if err != nil { ... }
if s.grouping != nil {
    if err := s.grouping.Reassign(ctx, beatID); err != nil {
        log.Printf("[WARN] grouping reassign beat=%d: %v", beatID, err)
        // 失敗不阻塞主流程
    }
}
```

`s.grouping` 走 constructor 注入；nil = 沒啟用 → 跳過。

### 2.4 接 grouping CRUD

CreateGrouping / UpdateGrouping / DeleteGrouping / ReorderGroupings 成功之後，handler 觸發 `engine.ReassignAll(ctx, 48*time.Hour)`。

### 2.5 ListBeats 改寫

`pkg/repository/beat.go:625` 附近：

```sql
SELECT b.*, ...,
       a.grouping_id AS assigned_grouping_id
FROM beats b
JOIN beat_members bm ...
LEFT JOIN beat_grouping_assignments a ON a.beat_id = b.id
WHERE 1=1
  -- 既有條件
GROUP BY b.id
HAVING (COUNT(bm.item_id) = 1 OR b.canonical_title IS NOT NULL)
   AND unread_count > 0
   AND (
        :group_id IS NULL AND a.grouping_id IS NULL
     OR :group_id IS NOT NULL AND a.grouping_id = :group_id
   )
ORDER BY ...
```

`ListBeats` 簽名加 `groupingID *int64`：

- `nil` → 過濾「沒被任何 grouping 認領」的（main inbox）
- `非 nil` → 過濾該 grouping_id

### 2.6 Handler

`beatsHandler` / `beatSearchHandler` 接 `?group=<slug>` query param：

- 查 `GetGroupingBySlug`，沒有就回 404
- 把 `groupingID` 傳進 `ListBeats`
- Template data 帶 `CurrentGrouping *Grouping`（nil = All beats）+ `Groupings []Grouping`（dropdown 用）+ 各 grouping 的 unread count（看下面）

每個 grouping 的 unread count：在 Server 層做一個 helper `groupingCounts(ctx) map[int64]int`，一次 query：

```sql
SELECT a.grouping_id, COUNT(*) FROM beat_grouping_assignments a
JOIN beats b ON b.id = a.beat_id
WHERE b.first_seen_at > :since
  AND (... unread filter ...)
GROUP BY a.grouping_id
```

### 2.7 UI — header dropdown

`server/templates/beats.html` 把 `.beats-header-title` 換成：

```html
<details class="grouping-switcher">
    <summary>
        {{if .CurrentGrouping}}{{.CurrentGrouping.Name}}{{else}}All beats{{end}}
        ({{.CurrentCount}})
    </summary>
    <ul class="grouping-menu">
        <li><a href="/beats">All beats ({{.AllCount}})</a></li>
        {{range .Groupings}}
        <li><a href="/beats?group={{.Slug}}">{{.Name}} ({{index $.GroupingCounts .ID}})</a></li>
        {{end}}
        <li><a href="/settings/groupings" class="meta">Manage…</a></li>
    </ul>
</details>
```

CSS 走 `.grouping-switcher details > summary`，跟現有 `.search-page-form` 並排在 header。Mobile：dropdown 撐滿寬度。

PTR 重整 + auto-mark-as-read 邏輯不動，已經是 page-scoped 不用改。

### 2.8 Tests

- `grouping.Engine.Reassign` table-driven：
  - 沒 grouping → assignment.grouping_id IS NULL
  - 一個 grouping 全配 → 拿那個
  - 兩個都配，第一個贏（first-match-wins 是核心，必測）
  - 部分 tag 配不上 → NULL
- `grouping.Engine.ReassignAll` 測 window 邊界（剛好過 48h 的 beat 不被動）
- `Repository.ListBeats` 加 group 過濾的 case：
  - `groupingID = nil` 不回有 assignment 的 beat
  - `groupingID = X` 只回 assigned to X
- Handler integration：`GET /beats?group=taiwan-politics` 回傳該 group 的 card；不存在的 slug 回 404

### 2.9 不要做

- 不要 entities 相關（PR 3）
- 不要 drag-to-reorder（PR 4）
- 不要 dropdown 的 entity tag autocomplete（PR 4）

---

## PR 3 — entity_worker（Phase C）

**目標：** 加上 LLM entity extraction，`Engine.Reassign` 用上 `items.entities`，groupings 可以匹配 `claude` / `spacex` 這類 tag。

### 3.1 Schema

```sql
ALTER TABLE items ADD COLUMN entities JSON DEFAULT '[]';
ALTER TABLE items ADD COLUMN entities_extracted_at DATETIME;
CREATE INDEX IF NOT EXISTS idx_items_entities_pending
    ON items(entities_extracted_at) WHERE entities_extracted_at IS NULL;
```

### 3.2 Config + feature gate

`config.yml`：

```yaml
entities:
  enabled:  false
  provider: "openai"     # 重用 existing LLM client
  model:    "gpt-4o-mini"
  batch:    20
```

`pkg/features/`：

```go
func EntitiesEnabled(cfg config.Config) bool {
    return cfg.Entities.Enabled && strings.TrimSpace(cfg.Entities.Provider) != ""
}
```

### 3.3 Worker

`pkg/scheduler/entity_worker.go`，仿 `embed_worker.go` 結構：

```go
type EntityExtractor interface {
    // Extract 一次接一批，回傳每個 item 對應的 entity 陣列。
    Extract(ctx context.Context, items []domain.ClassifiedItem) ([][]string, error)
}

type entityWorker struct {
    store     EntityStore     // ListPendingEntities, SaveEntities
    extractor EntityExtractor
    grouping  *grouping.Engine // 可 nil
    interval  time.Duration
    batch     int
}

func (w *entityWorker) tick(ctx) error {
    items, err := w.store.ListPendingEntities(ctx, w.batch)
    if err != nil || len(items) == 0 { return err }
    ents, err := w.extractor.Extract(ctx, items)
    if err != nil { return err }
    affectedBeats := map[int64]struct{}{}
    for i, item := range items {
        cleaned := normalizeEntities(ents[i])  // lowercase + dedup + 限白名單
        if err := w.store.SaveEntities(ctx, item.ID, cleaned); err != nil { ... }
        if beatID, ok := w.store.BeatForItem(ctx, item.ID); ok {
            affectedBeats[beatID] = struct{}{}
        }
    }
    if w.grouping != nil {
        for bid := range affectedBeats {
            _ = w.grouping.Reassign(ctx, bid)
        }
    }
    return nil
}
```

啟動點：`scheduler.Start` 裡 `if features.EntitiesEnabled(cfg) { go w.run(ctx) }`，仿 cleanup worker。

### 3.4 Extractor 實作

`pkg/scheduler/entity_extractor.go`：

```go
type llmEntityExtractor struct {
    client *openai.Client
    model  string
}

// Prompt 走嚴格輸出格式：
//
//   System: 你是 entity 抽取器。針對每篇文章抽出最多 5 個專有名詞 entities，
//   範圍限定：公司、產品、公眾人物、地點。輸出 JSON array of arrays，
//   外層長度 = 文章數，每個內層元素 lowercase。不要抽 generic noun
//   (e.g., "AI", "company", "election")，不要猜，不確定就跳過。
//
//   User:
//     [{"i":0,"title":"...","summary":"..."}, {"i":1,...}, ...]
//
//   Expected output: [["claude","anthropic"], ["spacex","starship"], ...]

func (e *llmEntityExtractor) Extract(ctx, items) ([][]string, error) { ... }
```

normalize：lowercase, trim, drop 長度 < 2 的 token, drop 純數字。

### 3.5 Engine 更新

`Repository.BeatTagSet` 改成 union `topics ∪ entities`：

```sql
SELECT DISTINCT json_each.value FROM items
LEFT JOIN beat_members ON beat_members.item_id = items.id
WHERE beat_members.beat_id = ?
  AND json_each.value IS NOT NULL
-- 用兩次 json_each: items.topics + items.entities，UNION ALL
```

實際寫法可能要拆兩段 query 在 Go side union；SQLite 的 `json_each` 在同一 query 裡組合多 column 比較囉嗦。

### 3.6 Tests

- `entityWorker.tick` 測：批次處理、save 失敗繼續處理下一個、Reassign 被觸發、affected beats 正確
- Extractor mock 測 prompt 組合 + 輸出 parse
- `BeatTagSet` 測 union（一個 item 有 topics ai/china、另一個 item 有 entities claude → 全部出現）
- 整合測：item 進來 → classify → entity_worker tick → grouping engine reassign → ListBeats(group=claude) 回傳該 beat

### 3.7 不要做

- 不要動既有 classifier（不要把 entities 塞進現有 prompt，分離得清楚）
- 不要為 entities 做 UI chip（ADR 說 entities 是匹配-only，不暴露給使用者）

---

## PR 4 — Polish（Phase D，optional）

照優先序：

1. **Drag-to-reorder grouping list**：vendor 一支極小的 sortable lib（或 native HTML5 drag），事件丟到 `POST /api/v1/groupings/reorder`，回傳更新後的 `groupings-list.html` swap。
2. **Tag autocomplete in grouping form**：撈 `items.topics` + `items.entities` distinct value 排序（cache），HTMX `hx-trigger="keyup changed delay:300ms"` 走 `/api/v1/tags/suggest?q=<prefix>` 回 datalist。
3. **Per-grouping unread badge in dropdown**：PR 2 已經有 count，PR 4 加紅點動畫。

---

## 跨 PR 注意事項

- **不要跑 `unfuck-ai-comments` 整庫掃過**（過往會動到不該動的檔案；要跑就只跑改到的檔）
- **Commit 用 lowercase**（CLAUDE.md convention）
- **不要加 Co-Authored-By / Generated with Claude Code trailer**
- PR description **不要加「Test plan」章節**
- Git author 若是 `Cluade`（typo），用 `git -c user.name="Claude" commit ...` 覆寫
- 每個 PR 結束都跑一次 completion sequence：`go test ./... && golangci-lint run ./... && go generate ./...`

## 驗收（Phase B 結束時）

- 建一個 grouping `Taiwan politics` tags=`taiwan, politics`，建一個 `Taiwan-China` tags=`taiwan, china`，order = 前者第一
- 一篇 taiwan + politics 的 beat → 出現在 `Taiwan politics`，主 inbox 看不到
- 一篇 taiwan + china 的 beat → 出現在 `Taiwan-China`
- 一篇 taiwan + politics + china 的 beat → 只在 `Taiwan politics`（first-match-wins 驗收的核心）
- 把 `Taiwan-China` 拖到第一 → ReassignAll 跑完後上一條改去 `Taiwan-China`
- 刪掉 grouping → 該 grouping 的 beats 回到主 inbox

## 驗收（Phase C 結束時）

- 開 `entities.enabled: true`、塞一篇 Claude 4.5 release 的 item
- 等 entity_worker tick → 該 item `entities` 出現 `claude`
- 建 grouping `Claude` tags=`claude` → 該 item 對應的 beat 出現在 `Claude` group
- 主 inbox 看不到該 beat
