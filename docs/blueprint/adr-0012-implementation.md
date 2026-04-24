# ADR 0012 實作說明

交給實作者的開發描述。設計決定已經在 `docs/adr/0012-topic-chip-inline-navigation.md`，這裡只講怎麼落地。

## 必讀先修

- `docs/adr/0012-topic-chip-inline-navigation.md`（7 項決定全部在這）
- `docs/adr/0011-beats-ui-design.md`（beats UI 既有 baseline）
- `server/templates/article-card.html` L39-50：現有 `username › topic` 實作（HTMX 但沒 push-url，是本次要改的對象）
- `server/templates/beat-card.html` L1-12, L76-88：beat-card 現在是 **整卡可點擊**（gemini 的 slide-right 詳情）；trigger 已排除 `.card-actions` + `.clickable-topic`，新增的連結要走 `.clickable-topic` 這條 class 路徑才不會誤觸整卡導覽

## Scope（按檔案）

### 1. Repository 層：big tag 計算

**新增** `repository/classification.go`（或類似位置）：

```go
GetBigTags(ctx context.Context, threshold int) (map[string]int, error)
```

- 統計 `classifications.topics` JSON 欄位每個 tag 出現次數
- 回傳 `count >= threshold` 的 tag → count map
- 預設 threshold=5（硬編，ADR 有記錄未來再調）

**Cache：** server 層加一個 TTL cache（例如 5 分鐘），避免每次 render 都查。

### 2. Handler 層：把 big tag map 傳進 template data

下列 handler 的 template data struct 都要多一個 `BigTags map[string]struct{}`（只查存在性，value 不重要）：

- `articlesHandler`（`server/htmx_handlers.go:141`）
- `sourceHandler`（`server/htmx_handlers.go:1354`）
- `beatsHandler`
- `beatDetailHandler`
- `searchHandler`
- `beatSearchHandler`（`server/htmx_handlers.go:1530`）

Template helper function 加 `isBigTag` 讓模板判斷。

### 3. Domain 層：beat primary topic

`pkg/domain/beat.go` 的 `BeatWithMembers` 加一個 method：

```go
func (b *BeatWithMembers) PrimaryTopic() string
```

- 統計所有 members 的 `Topics`，回傳出現最多次的
- tie 時取第一個遇到的
- 空 topics 回傳 `""`

### 4. Templates

#### `article-card.html`

L39-50 header chip：
- `<span class="card-head-sep">›</span>` 保留
- 大 tag：link 加 class `topic-chip-big`、文字變 `#{{$primaryTopic}}`
- 加上 `hx-push-url="true"`

L89-102 底部 `.topics` 列：
- 大 tag 同樣加 `topic-chip-big` class + `#` 前綴
- 加上 `hx-push-url="true"`

#### `beat-card.html`

L76-88 header 區：
- 仿 article-card 加 `› #topic` 行，資料來源 `.PrimaryTopic`
- 連結要用 `.clickable-topic` class（否則會被整卡 hx-get 攔截）

L79-81 feed-name：
- 多成員：保留 `<span>{{$membersCount}} sources</span>`
- 單成員：改成
  ```html
  <a href="/source/{{(index .Members 0).FeedName}}"
     class="feed-name clickable-topic">{{(index .Members 0).FeedName}}</a>
  ```
  必須加 `.clickable-topic` 才不會觸發整卡導覽

#### `beat-detail.html`

同樣加 header `› #topic` chip。

### 5. CSS（`server/static/css/style.css`）

新增（顏色 token 對齊現有設計系統）：

```css
.topic-chip-big {
    background: var(--accent-blue, #3b82f6);
    color: white;
    padding: 2px 8px;
    border-radius: 12px;
    font-size: 0.8125rem;
    text-decoration: none;
}
.topic-chip-big:hover { opacity: 0.85; }
```

### 6. 「點大 tag = 清除 filter」行為

- 當前 URL 已經是 `?topic=X` 且使用者點的就是 X → 連結 target 變成同頁但 **不帶 `?topic=` 參數**
- 邏輯放 template：根據目前 `.SelectedTopic`（或類似欄位）判斷 `$primaryTopic == .SelectedTopic`

## Tests

- `Repository.GetBigTags` unit test（表驅動：tag count 分佈 + threshold 邊界）
- `BeatWithMembers.PrimaryTopic` unit test（tie 處理、空 topics、多 members）
- Handler 測試：mock big tags → template render 包含 `.topic-chip-big` class
- Template render 測試：beat-card 單成員 feed-name 為 `<a>`，多成員為 `<span>`

## 非範圍（不要做）

- ❌ 不做 section header 分組（ADR 決定，`#` chip + 底色就夠了）
- ❌ 不動 `/search` 的模板（共用 `article-card.html`，自然繼承）
- ❌ 不刪 `/articles`（另一個題目）
- ❌ 不改 `beats.html` search form 的 URL
- ❌ 不做 tag grouping 的 sidebar / navigation UI

## 驗收

- `/articles` 打開，某篇文章 primary topic 是大 tag → 該 chip 為 `#tag` 藍底；點擊 URL 變 `/articles?topic=tag` 且頁面顯示 filter 結果
- `/beats` 打開，每張 beat 卡 header 有 `feed-name › #topic`；點大 tag chip → URL push；點 feed-name（單一 source）→ 導向 `/source/{name}`；點卡片其他地方仍走 slide-right 詳情
- `?topic=ai` 中點同一個 `#ai` chip → 清除 filter 回到 `/articles`
- Build / test / lint 全綠

## 注意事項

- **不要跑 `unfuck-ai-comments 整庫掃過`**（過往經驗：會動到不該動的檔案）
- **Commit 用 lowercase**（專案 convention，見 `CLAUDE.md`）
- **不要加 Co-Authored-By / Generated with Claude Code trailer**
- Git author 若是 `Cluade`（typo），用 `git -c user.name="Claude" commit ...` 覆寫
- PR description 不要加「Test plan」章節
