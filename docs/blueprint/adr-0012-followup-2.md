# ADR 0012 Follow-up Fixes (第 2 輪)

基於 `feat/topic-chip`（commit `4073e8f`）測試後發現三個 bug。

## 必讀

- `docs/adr/0012-topic-chip-inline-navigation.md`
- `docs/blueprint/adr-0012-implementation.md`（第 1 輪 handoff）
- `docs/blueprint/adr-0012-followup.md`（第 2 輪 handoff，已完成）
- 本輪基底：branch `feat/topic-chip`，在這條 branch 上追加 commit

## Bug A — 點 chip URL 變但 filter 沒生效（`/beats?topic=`）

**現象：** 在 `/beats` 點 `#china` chip → URL 變 `/beats?topic=china`，但顯示的 beats 完全沒過濾、跟點之前一樣。

**根因：**
1. `server/htmx_handlers.go:1428` `beatsHandler` 完全不讀 `r.URL.Query().Get("topic")`，從頭到尾只呼叫 `s.db.ListBeats(ctx, pageSize, offset)`
2. `pkg/repository/beat.go:602` `ListBeats(ctx, limit, offset)` SQL 沒有 topic 篩選子句（只 `HAVING COUNT... OR canonical_title IS NOT NULL`）
3. `beatSearchHandler`（1583）也是一樣問題

**修法：**

### Repository 層

`pkg/repository/beat.go` `ListBeats` 加 topic 篩選。兩種選擇：

- **(a)** 加參數：`ListBeats(ctx, topic string, limit, offset int)`（空字串 = 不過濾）
- **(b)** 新增 `ListBeatsByTopic(ctx, topic string, limit, offset int)`，保留原 `ListBeats`

建議 (a)，call site 少，改一次就好。SQL WHERE 加：
```sql
AND EXISTS (
    SELECT 1 FROM beat_members bm2
    JOIN items i2 ON i2.id = bm2.item_id, json_each(i2.topics)
    WHERE bm2.beat_id = b.id AND json_each.value = ?
)
```
（或更簡潔：`EXISTS (SELECT 1 FROM items i2 JOIN beat_members bm2 ON bm2.item_id = i2.id, json_each(i2.topics) WHERE bm2.beat_id = b.id AND json_each.value = ?)`，自行優化）

`SearchBeats` / `SearchWithMembers` 同樣加 topic 參數（如果搜尋頁也支援 topic filter）。

Database interface（`server/server.go` 附近）signature 也要更新。

### Handler 層

`beatsHandler`：
```go
topic := strings.TrimSpace(r.URL.Query().Get("topic"))
beats, err := s.db.ListBeats(ctx, topic, pageSize, offset)
```

`beatSearchHandler`：一樣。

`commonPageData.SelectedTopic` 要填上 `topic`（供 template 做 clear-filter 邏輯用，雖然 beat-card 目前沒實作，但統一資料流）。

### Mocks

`server/mocks/database.go` 的 `ListBeatsFunc` 簽章跟著改，`ListBeatsMock` struct 加新欄位。`go generate ./...` 或手動改 mock 檔（generated 的就重新 generate）。

### Tests

- `TestBeatRepository_ListBeats` 加 case：指定 topic → 只回傳該 topic 的 beats；不指定（空字串）→ 全部
- Handler 測試：`/beats?topic=ai` → 只有 ai beats

## Bug B — `/beats?topic=X` 和 `/source/{name}` 標題 + 返回鍵

**現況：** 兩個頁面 header 還是全站標題（Beats / feedName），沒有 context，也沒返回鍵。

**期望：**
- `/beats?topic=china` → header 左側 `<` 返回（目的地 `/beats`），標題 `#china`
- `/source/{name}` → header 左側 `<` 返回（目的地 `/articles`，或 `/beats` — 見「決策點」），標題 `{name}`

**修法：** 塞 `commonPageData.PageTitle` + `BackURL`，`base.html` 已經會自動 render（見 `server/templates/base.html:215-235`）。

### `beatsHandler`
```go
data := struct {
    commonPageData
    ...
}{
    commonPageData: commonPageData{
        ActivePage: "beats",
        PageTitle:  func() string { if topic != "" { return "#" + topic }; return "" }(),
        BackURL:    func() string { if topic != "" { return "/beats" }; return "" }(),
        SelectedTopic: topic,
    },
    ...
}
```

HTMX 回應尾端手寫的 `<h2 id='page-title'...>Beats</h2>` OOB block（1483-1484）也要改成 dynamic：
```go
if topic != "" {
    fmt.Fprintf(w, `<h2 id='page-title' class='page-title' hx-swap-oob='true'><span class="title-text">#%s</span></h2>`, html.EscapeString(topic))
    fmt.Fprintf(w, `<div id='header-back' class='header-left' hx-swap-oob='true'><a href='/beats' class='back-button' hx-get='/beats' hx-target='main.container' hx-push-url='true' hx-swap='innerHTML' title='返回'><svg width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'><path d='m15 18-6-6 6-6'/></svg></a></div>`)
} else {
    // 現狀：Beats 標題、空 header-back
}
```
（醜是醜，但維持跟 rsshub/beat-detail 一致的 OOB pattern。若想抽 helper 函式也可以。）

### `sourceHandler`
類似：PageTitle=`feedName`、BackURL=`/articles`（或 `/beats`，見下）。

## Bug C — `/source/{name}` 白畫面

**現象：** 點 beat-card 單一 source 的 feed-name 超連結 → 白畫面。Log：
```
[WARN] failed to render source page: template: "" is an incomplete or empty template
```

**根因：** `server/htmx_handlers.go:1422` 用的是 `tmpl.Execute(w, data)` — 跑 root（空名）template。`source.html` 內容只有 `{{template "base.html" .}}` + 兩個 `{{define}}` block，root template 是空的。

對照 `beatsHandler:1476` 是 `s.renderPage(w, "beats.html", data)`，內部 `ExecuteTemplate(w, "beats.html", data)` 正確跑帶名稱的 template。

**修法（1 行）：**
```go
// server/htmx_handlers.go:1421-1424 改成：
if err := s.renderPage(w, "source.html", data); err != nil {
    log.Printf("[WARN] failed to render source page: %v", err)
    s.respondWithError(w, http.StatusInternalServerError, "Failed to render page", err)
    return
}
```

同時 `data` struct 要 embed `commonPageData` 才能讓 base.html 讀到 `.PageTitle` / `.BackURL` / `.ActivePage`：
```go
data := struct {
    commonPageData
    FeedName       string
    UnreadArticles []articleCardData
    ReadArticles   []articleCardData
}{
    commonPageData: commonPageData{
        ActivePage: "feeds",         // 或新增一個 "source"，讓 sidebar active state 合理
        PageTitle:  feedName,
        BackURL:    "/articles",     // 見決策點
    },
    FeedName:       feedName,
    UnreadArticles: wrapArticleCards(unreadArticles, ""),
    ReadArticles:   wrapArticleCards(readArticles, ""),
}
```

**注意：** 這個 bug 其實是**既有（pre-existing）**，commit c00d693 之前就這樣寫。但使用者之前應該沒從 UI 點過 `/source/{name}`，直到 ADR-0012 在 beat-card 加了單一 source feed-name 連結才觸發。article-card 的 feed-name link（L37）其實一直都會中，只是沒人點過。

## 決策點

**`/source/{name}` 的 BackURL 指哪？**
- `/articles` — 因為傳統 UI，source 頁面是從 article 點過去的
- `/beats` — 使用者現在主要瀏覽 beats，從 beat-card 單成員點過去

ADR 沒明確寫，建議讓 BackURL 用 `Referer` header（Go `r.Referer()`），fallback `/articles`。

## Tests

- `TestBeatRepository_ListBeats` 加 topic 過濾 case
- `TestServer_beatsHandler` 加 `?topic=ai` case：只回傳有 ai topic 的 beats
- `TestServer_sourceHandler` 加 render 成功案例（確保 render 不回 template 錯誤）
- 已有的 `TestServer_sourceHandler` 如果 mock DB 沒設 `GetClassifiedItemsWithFiltersFunc` 要補

## 驗收

- `/beats?topic=china` → 只顯示有 china topic 的 beats；header 標題 `#china` + 左側 `<` 返回 `/beats`
- 點任何大 tag chip → URL push + filter 實際生效
- 從 beats/articles 點 source feed-name → 不再白屏，正常顯示 source 頁面
- source 頁面 header 標題 = source 名稱 + 左側 `<` 返回
- build / test / lint 全綠

## 注意事項

- 不要跑 `unfuck-ai-comments 整庫掃過`
- Commit 用 lowercase
- 不要加 Co-Authored-By / Generated with Claude Code trailer
- Git author 若是 `Cluade`（typo），用 `git -c user.name="Claude" commit ...` 覆寫
- PR description 不要加 Test plan 章節
- 在 `feat/topic-chip` 分支上 append commit，不要 rebase 或 squash 已有 commit
