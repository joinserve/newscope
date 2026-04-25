# Beat timeline 實作說明

把單一 beat 的演進變成可看的時間軸，並（後續）讓跨 beat 的續篇連起來。
ADR 等之後決定要不要正式寫，目前先把設計理由寫在這個 blueprint 開頭。

## 必讀先修

- `docs/adr/0010-cross-item-deduplication-and-merge.md`（beats backend、48h window 為什麼存在）
- `docs/adr/0011-beats-ui-design.md`（beat detail / comment-expand 的既有切分）
- `docs/adr/0013-beat-groupings.md`（groupings + entity 抽取的整體脈絡）
- `pkg/scheduler/merge_worker.go`（canonical title/summary 產生的地方，要在這加 revision append）
- `pkg/repository/beat.go`（ListBeats / GetBeat / member 載入）
- `server/templates/beat-detail.html`（要改的頁面）

## 背景與設計理由

beat 的 canonical title/summary 會隨著新 member attach 而被 LLM 重新產生（PR 7
of ADR 0010）。今天「重新產生」就是覆寫，沒有歷史。我們希望把「這條 story
怎麼演進」變成可看的東西，分兩層：

1. **Intra-beat**：同一個 beat 內，按時間看 title revisions + 各 member arrival
2. **Cross-beat**：48h window 外，明顯是同一條故事線的下一拍 → 用 `parent_beat_id` 連起來

兩層共用同一個視覺：detail 頁的 timeline section。Cross-beat 用 dashed line 切
開，視覺上是「這條故事線怎麼走」，使用者不用懂 beat 邊界。

### 關鍵決定（compressed）

- **D1**：title revisions 存在 append-only `beat_title_revisions` 表，**不**從 git
  log 風格的 history 推。原因：merge_worker 不見得每次都改 title（debounce），
  我們要記的是「LLM 真的給出新 title 的時刻」，需要明確 row。
- **D2**：cross-beat 用單一 nullable `beats.parent_beat_id`，遞迴往上撈 1–2 層即
  可（再多就拿到「整個敘事弧」獨立頁面去顯示）。
- **D3**：timeline 只放在 detail 頁。Comment-button expand 維持 flat members 列表
  （快速一瞥）。兩種 entry 分工，避免重複。
- **D4**：inbox card 只有在 `parent_beat_id IS NOT NULL` 時加 chip — intra-beat 的
  存在用既有 comment count 暗示就好，card 維持極簡。
- **D5**：cross-beat 偵測 = merge_worker 多一段 LLM judge，candidate set 限定在
  「過去 14 天 + 有 entity overlap」的 beats。所以 **Phase B 必須等 ADR 0013 PR 3
  entities 上線**才有意義（否則 candidate set 太大、judge 成本爆）。

## 拆 PR

兩階段、四個 PR。Phase A 可以獨立 ship 並跑一陣子收 UX feedback；Phase B 等
groupings 系列收尾再來。

| PR | 範圍                                | 依賴                          |
|----|-------------------------------------|-------------------------------|
| 1  | `beat_title_revisions` schema + 寫入 | merge_worker                  |
| 2  | Detail 頁 timeline 渲染              | PR 1                          |
| 3  | `parent_beat_id` schema + LLM judge  | ADR 0013 PR 3 (entities)     |
| 4  | Timeline 延伸 + card chip            | PR 3                          |

---

## PR 1 — `beat_title_revisions` 表 + merge_worker append

**目標**：每次 merge_worker 跑出新 canonical 時 append 一筆 revision row。讀取面
不動，純資料層 + worker。

### 1.1 Schema

`pkg/repository/schema.sql` 新增：

```sql
CREATE TABLE IF NOT EXISTS beat_title_revisions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    beat_id      INTEGER NOT NULL REFERENCES beats(id) ON DELETE CASCADE,
    title        TEXT    NOT NULL,
    summary      TEXT    NOT NULL,
    generated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_beat_title_revisions_beat
    ON beat_title_revisions(beat_id, generated_at);
```

純 append-only，沒有 update/delete 路徑（除了 cascade）。

### 1.2 Repository

`pkg/repository/beat.go` 加：

```go
type TitleRevision struct {
    ID          int64
    BeatID      int64
    Title       string
    Summary     string
    GeneratedAt time.Time
}

// AppendTitleRevision adds a new title/summary snapshot for a beat.
// idempotent on (beat_id, title, summary): if the most recent revision
// matches exactly, no row is inserted.
func (r *BeatRepository) AppendTitleRevision(ctx context.Context, beatID int64, title, summary string) error
```

**Idempotency**：merge_worker 可能在沒有真實內容變化時跑（debounce 失敗 / 競
態）。append 之前先 SELECT 最後一筆，title+summary 相同就 skip。避免時間軸
出現「14:30 改成 X、14:31 又改成 X」這種噪訊。

```go
// ListTitleRevisions returns revisions ordered by generated_at ASC.
func (r *BeatRepository) ListTitleRevisions(ctx context.Context, beatID int64) ([]TitleRevision, error)
```

PR 2 用得到，這 PR 順便寫好。

### 1.3 接 merge_worker

`pkg/scheduler/merge_worker.go` 在 `SaveCanonical` 成功之後：

```go
if err := s.store.SaveCanonical(ctx, beatID, canonical); err != nil { ... }
if err := s.store.AppendTitleRevision(ctx, beatID, canonical.Title, canonical.Summary); err != nil {
    log.Printf("[WARN] append title revision beat=%d: %v", beatID, err)
    // 失敗不阻塞主流程
}
```

**Backfill**：現有 beats 的第一次 canonical 沒有 revision row。一次性 migration（可
寫成 `cmd/` 一次性 tool 或在 schema migration 裡）：

```sql
INSERT INTO beat_title_revisions (beat_id, title, summary, generated_at)
SELECT id, canonical_title, canonical_summary,
       COALESCE(canonical_merged_at, first_seen_at)
FROM beats
WHERE canonical_title IS NOT NULL
  AND id NOT IN (SELECT DISTINCT beat_id FROM beat_title_revisions);
```

### 1.4 Tests

- `AppendTitleRevision` 表驅動：第一筆寫入、相同內容 skip、內容變了 append、不同 beat 互不影響
- `ListTitleRevisions` 排序測試
- merge_worker 整合測試：跑兩輪不同內容 → 兩筆 revision；跑兩輪相同內容 → 一筆

### 1.5 不要做

- 不要寫 detail 頁渲染（PR 2）
- 不要動 `parent_beat_id`（PR 3）
- 不要在 inbox card 顯示 revision count

---

## PR 2 — Detail 頁 timeline 渲染

**目標**：把 detail 頁從「canonical + 平鋪 members」改成「canonical + 時間軸
（按 revision 分組，每組下面列當段 members）」。

### 2.1 Domain / handler 資料準備

`pkg/domain/beat.go` 加一個 view-model struct（不存 DB）：

```go
type TimelineSegment struct {
    Revision TitleRevision  // 該段標題
    Members  []ClassifiedItem  // 此 revision 之後到下一 revision 之前 attach 的 members
    IsCurrent bool  // 最新一段
}

type BeatTimeline struct {
    Segments []TimelineSegment  // 由新到舊
}
```

`server/htmx_handlers.go:beatDetailHandler` 拉資料邏輯：

1. `ListTitleRevisions(ctx, beatID)` → revisions ASC
2. `GetBeat(ctx, beatID)` → members
3. 在 Go side 分桶：
   - 對每個 revision，找出 `added_at` ∈ `[revision.GeneratedAt, nextRevision.GeneratedAt)` 的 members
   - 最後一個 revision 的右端是 +∞
   - **Edge case**：member 早於第一個 revision（例如 backfill 的 beat、或第一個 member 進來但還沒跑 merge_worker）→ 歸到第一個 revision 那一段
4. Reverse → segments[0] 是最新

把 `BeatTimeline` 塞進 template data。

### 2.2 Template 改動

`server/templates/beat-detail.html`：

把現有 members 區塊換成：

```html
<section class="beat-timeline">
    <h3 class="timeline-heading">↻ How this story developed</h3>

    {{range .Timeline.Segments}}
    <div class="timeline-segment {{if .IsCurrent}}current{{end}}">
        <div class="timeline-node">
            <time>{{.Revision.GeneratedAt | formatRelativeDay}}</time>
            <h4 class="revision-title">{{.Revision.Title}}{{if .IsCurrent}} <span class="badge-current">current</span>{{end}}</h4>
        </div>
        <div class="timeline-members">
            {{range .Members}}
            <article class="timeline-member">
                <time>{{.AddedAt | formatTime}}</time>
                <span class="feed-name">{{.FeedName}}</span>
                <a href="{{.Link}}" target="_blank">{{.Title}}</a>
            </article>
            {{end}}
        </div>
    </div>
    {{end}}
</section>
```

`formatRelativeDay`：`今天 14:30` / `昨天 10:00` / `Mon 10:00` / `Apr 18 10:00`。
新 funcMap helper（小函式，放在 server.go 既有 funcMap 區塊）。

**Comment-button expand 不動** — 維持 flat members 列表 (ADR 0011 既有路徑)。
兩個入口分工。

### 2.3 CSS

`server/static/css/style.css` 新增：

- `.beat-timeline` — section padding
- `.timeline-segment` — 左邊 border 模擬 timeline 軸
- `.timeline-segment.current .timeline-node h4` — 加重
- `.timeline-node` — 標題列樣式，含 time + h4
- `.badge-current` — 小膠囊
- `.timeline-members` — 縮排
- `.timeline-member` — 單行排版（time + feed + link）

對照 ADR 0011 既有的 `.beat-card` 系列調色。

### 2.4 Tests

- `TestBeatDetailHandler_TimelineSegmentation`：fixture 三筆 revisions、五個 members 分散在不同時間 → assert segments 長度 + 每段 members 正確
- `TestBeatDetailHandler_MemberBeforeFirstRevision`：edge case — member added_at 早於最早的 revision，歸到第一段
- Template render 測試：渲染後 HTML 包含 `current` class 在第一段

### 2.5 不要做

- 不要動 comment-button / beatMembersHandler（維持 flat 模式）
- 不要在 inbox card 顯示任何 timeline-related UI
- 不要在 timeline 加 like/dislike 按鈕（feedback 是 beat-level，已經在 detail 頁頂端）
- 不要支援展開/收合每個 segment（先全展開，太長再說）

### 2.6 驗收

- 開一個有多個 members + 多次 revision 的 beat，detail 頁看到時間軸結構
- 最新 revision 標 `current`
- Members 正確分到對應的 revision 段下面
- Comment-button 仍然展開 flat 列表（行為沒變）

---

## PR 3 — `parent_beat_id` 偵測與儲存

**前置**：ADR 0013 PR 3 entities 上線後才做。如果 entities 還沒做完，這 PR 整
個延後。

### 3.1 Schema

```sql
ALTER TABLE beats ADD COLUMN parent_beat_id INTEGER REFERENCES beats(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_beats_parent ON beats(parent_beat_id);
```

`ON DELETE SET NULL` — parent 被刪不該連動殺後續 beat，斷掉鏈即可。

### 3.2 Judge interface

`pkg/scheduler/parent_judge.go`（新檔，仿 entity_extractor 結構）：

```go
type ParentJudge interface {
    // FindParent decides whether `beat` continues an earlier story.
    // candidates is bounded (≤ 10) and pre-filtered by entity overlap + time window.
    // Returns parentBeatID and a confidence string ("definite" / "likely" / "unrelated").
    // Empty parentBeatID + "unrelated" means no link.
    FindParent(ctx context.Context, beat domain.Beat, candidates []domain.Beat) (int64, string, error)
}

type llmParentJudge struct {
    client *openai.Client
    model  string
}
```

Prompt 大致樣態（嚴格輸出）：

```
System: 你判斷一個新 news beat 是不是某個更早 beat 的續篇。
續篇 = 同一條敘事弧的下一拍（同公司/事件的後續發展）。
NOT 續篇 = 同產業 / 同主題但獨立事件。

Input:
{
  "new_beat": {"title": "...", "summary": "..."},
  "candidates": [
    {"id": 12, "title": "...", "summary": "...", "days_ago": 3},
    ...
  ]
}

Output JSON:
{"parent_id": 12, "confidence": "likely"}
or
{"parent_id": null, "confidence": "unrelated"}
```

### 3.3 Candidate selection（repository）

`pkg/repository/beat.go` 加：

```go
// CandidateParents returns beats from the past `window` whose member entity set
// overlaps with `entityHints` by at least one. Bounded by `limit`.
func (r *BeatRepository) CandidateParents(
    ctx context.Context,
    beatID int64,
    entityHints []string,
    window time.Duration,
    limit int,
) ([]domain.Beat, error)
```

SQL 概念（會比較囉嗦，因為要 union entities + topics 然後算 overlap）：

```sql
WITH new_beat_entities AS (
    SELECT DISTINCT json_each.value AS ent
    FROM beat_members
    JOIN items ON items.id = beat_members.item_id
    JOIN json_each(items.entities)
    WHERE beat_members.beat_id = :beatID
)
SELECT b.id, b.canonical_title, b.canonical_summary, b.first_seen_at
FROM beats b
JOIN beat_members bm ON bm.beat_id = b.id
JOIN items i ON i.id = bm.item_id
JOIN json_each(i.entities) je
WHERE b.id != :beatID
  AND b.first_seen_at > :since
  AND je.value IN (SELECT ent FROM new_beat_entities)
GROUP BY b.id
ORDER BY b.first_seen_at DESC
LIMIT :limit;
```

實作可能拆兩段在 Go side 組，看寫起來舒服哪種。

### 3.4 接 merge_worker

merge_worker 在 `SaveCanonical` + `AppendTitleRevision` 之後，多一段：

```go
if s.parentJudge != nil {
    candidates, err := s.store.CandidateParents(ctx, beatID, entityHints, 14*24*time.Hour, 10)
    if err == nil && len(candidates) > 0 {
        parentID, conf, err := s.parentJudge.FindParent(ctx, beat, candidates)
        if err == nil && parentID > 0 && (conf == "definite" || conf == "likely") {
            if err := s.store.SetParent(ctx, beatID, parentID); err != nil {
                log.Printf("[WARN] set parent beat=%d parent=%d: %v", beatID, parentID, err)
            }
        }
    }
}
```

`s.parentJudge` 走 constructor 注入；nil = 沒啟用 → 跳過。Feature flag：
`stories.enabled` in config。

**只在新 beat 第一次 canonical 時跑** — 第二次以後 canonical 變化通常是同
beat 內聚合更多 members，parent 不該改。merge_worker 已經知道是不是首次（看
`canonical_merged_at` 是否為 NULL），加個 guard 即可。

### 3.5 Tests

- `CandidateParents` 測：entity overlap 過濾正確、window 邊界、limit 生效
- `parentJudge` mock + merge_worker 整合：candidates 給三個 + judge 回 parent_id=2 → DB 寫入正確
- `judge` 回 unrelated → DB 不寫
- 第二次 canonical（已有 `canonical_merged_at`）→ judge 不被呼叫

### 3.6 不要做

- 不要支援 parent 鏈長度 > 2（爺爺 → 父 → 子）— 顯示複雜度爆
- 不要做「使用者手動連結」UI（太 niche）
- 不要做「斷開連結」UI（同上）

---

## PR 4 — Timeline 延伸 + card chip

**目標**：把 cross-beat 的 parent 接到 detail 頁 timeline 下方，inbox card 加
chip。

### 4.1 Detail 頁延伸

`beatDetailHandler` 多撈：

```go
// 遞迴往上撈 parent，最多 2 層
parents, err := s.db.GetParentChain(ctx, beatID, 2)
```

Repository：

```go
func (r *BeatRepository) GetParentChain(ctx context.Context, beatID int64, maxDepth int) ([]domain.Beat, error)
```

每個 parent 也撈它的 revisions（複用 `ListTitleRevisions`），但 detail 頁**只
顯示該 parent 的最新 revision** 作為 compact `◇` row（不展開該 parent 的
members，要看完整內容點 `[open]` 跳過去）。

Template 在 timeline section 末尾加：

```html
{{if .ParentChain}}
<div class="timeline-divider" aria-hidden="true">earlier in this story</div>
{{range .ParentChain}}
<div class="timeline-segment-parent">
    <div class="timeline-node">
        <time>{{.FirstSeenAt | formatRelativeDay}}</time>
        <h4 class="parent-title">{{.CanonicalTitle}}</h4>
        <span class="parent-meta">{{.MemberCount}} sources</span>
    </div>
    <a class="timeline-open" href="/beats/{{.ID}}">open →</a>
</div>
{{end}}
{{end}}
```

CSS：`.timeline-divider` 是 dashed line + 中間文字；`.timeline-segment-parent`
比 `.timeline-segment` 視覺上輕（淡色 + 不展開 members）。

### 4.2 Card chip

`server/templates/beat-card.html` 在 header `feed › #topic` 那行：

```html
{{if .ParentBeatID}}
<span class="card-head-sep">·</span>
<a class="card-head-chip continues-chip clickable-topic"
   href="/beats/{{.ID}}#timeline-divider">
   ↻ continues ({{.PriorCount}})
</a>
{{end}}
```

`.PriorCount` = parent chain 長度（往上遞迴撈到 root），repository 提供：

```go
func (r *BeatRepository) PriorBeatCount(ctx context.Context, beatID int64) (int, error)
```

**Anchor 跳轉**：detail 頁的 `.timeline-divider` 加 `id="timeline-divider"`，chip
連結直接帶 anchor，使用者點下去 detail 頁 scroll 到 dashed line。

### 4.3 Tests

- Template render：`ParentBeatID = nil` → 沒 chip；`ParentBeatID = 5` → chip
  顯示且連結含 `#timeline-divider`
- Detail 頁測試：`ParentChain` 長度 2 → 渲染 2 個 `timeline-segment-parent`
- Anchor scroll 在 e2e 層只測 anchor 字串存在，不模擬瀏覽器行為

### 4.4 不要做

- 不要在 timeline 內展開 parent beat 的 members（壓縮成 row + open 連結就好）
- 不要支援「往下找後續 beat」（child beat） — 這是 view 的方向問題，現在這個方
  向（往上找 parent）已經夠用
- 不要做 entity 頁 / story 頁（dedicated route 是後話）

---

## 跨 PR 注意事項

- Commit 用 lowercase
- 不要加 Co-Authored-By / Generated with Claude Code trailer
- PR description 不要加「Test plan」章節
- 每個 PR 結束跑：`go test ./... && golangci-lint run ./... && go generate ./...`
- 不要 `unfuck-ai-comments` 整庫掃過，只跑改到的檔
- Git author 若是 `Cluade`（typo），用 `git -c user.name="Claude" commit ...` 覆寫
- PR 1 / PR 2 可以連著做（沒外部依賴）
- PR 3 / PR 4 等 ADR 0013 PR 3 entities 上線再啟動

## 整體驗收（Phase A 結束）

- 任意 beat 的 detail 頁看到 `↻ How this story developed` section
- Section 內按時間倒序列 revisions，每個 revision 下面是該段時間內 attach 的 members
- 最新 revision 標 `current`
- Comment button 點開仍是 flat members 列表（沒被破壞）
- inbox card 沒有任何 timeline 相關 chip（PR 4 才加）

## 整體驗收（Phase B 結束）

- 一個有 parent 的 beat，inbox card 顯示 `↻ continues (N)` chip
- 點 chip → 跳到 detail 頁，scroll 到 dashed line
- Dashed line 下方按時間倒序列 parent chain（最多 2 層），每個 row 點 `open` 跳過去那個 beat
- 沒 parent 的 beat 完全沒有 chip 也沒 dashed line（card / detail 都 fallback 到 Phase A 樣態）
