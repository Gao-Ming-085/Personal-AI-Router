<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# 引擎代理（繁體中文譯本）

本檔為 [README.md](README.md) 之繁體中文節譯，目前涵蓋下列章節；
其餘內容以英文版為準。

## Strata 應答淨化

Strata 的 UD 對話模板可能針對請求中從未宣告的工具輸出 `<tool_call>`
標記。引擎將該標記視為遭拒的工具呼叫：它不做任何移除，只在原始應答中以
`strata_rejected_tool_calls` 標記，且——當模板沒有其他可答內容時——會把
該標記直接當作答案文字回傳。忽略此標記的客戶端會把自己的控制 token 當成
對話內容重新吸收，等於在訓練模型刻意洩漏它們。

當被路由的引擎選擇啟用（`engineProfile.SanitizeToolCallLeaks`，僅 Strata）
時，facade 會在應答回程檢查內容——本機與跨機器請求一律涵蓋，明文本機
監聽端與 mTLS 叢集入口兩處皆生效：

- **非串流**：移除該標記；清除每個 choice 內容中洩漏的區塊；若拒絕之後
  已無有效內容，以一句簡短說明取代標記。
- **串流**：在標記或可能的標記開頭出現前，delta 原樣通過。之後尾段進入
  暫存（不完整的標記前綴絕不外送），串流結束時暫存文字依狀況以淨化後
  內容或該說明送出。串流一律以 `[DONE]` 收尾。
- **未帶標記**的應答絕不改寫；任何解析失敗的內容逐位元組原樣通過；
  非推論路由完全不受影響。

淨化器不重試、不重問：它只從引擎已生成的內容中移除控制標記。復原行為
（以修正指令再次詢問模型）屬於客戶端的職責，而非代理。

## Strata 引擎

Strata 是一等引擎身份（`engines.Strata`）。它前方的 facade 同樣遵循「一引擎
一 facade」規則，但 port 規劃是特定的：Strata facade 綁定基礎 port
**18080**，engine-manager 以 loopback 啟動設定檔在 **127.0.0.1:18081**
啟動 `strata serve`——刻意不使用 llama.cpp 預設的 8080，兩引擎得在同一機器
共存。

兩項 Strata 專屬契約：

- **啟動設定檔不帶金鑰。** engine-manager 的健康探測不附
  `Authorization` header，因此由 PAIR 託管的 Strata 設定檔綁定 loopback 並
  省略 `api_key`；驗證留在邊界（上游客戶端或 guard 持有金鑰），引擎原始
  port 不外洩到機器之外。
- **預設開啟應答淨化**——見上文「Strata 應答淨化」。

遠端 Strata 機器（以及任何跑在非預設 port 的 OpenAI 相容引擎）透過手動
節點加入，使用 `lmstudio_port` 與選填的 `lmstudio_api_key`；見
[nvpair-manual-nodes](../nvpair-manual-nodes/README.md)。

## 選配：客戶端工具呼叫守衛

上一節的淨化器清除引擎已產生的標記；互補的一層在客戶端、請求進入路由器
之前：外部守衛（例如 `safeagents`）可在工具呼叫進入路由前，拒絕指向未宣告
工具或未知主機的呼叫，分為僅記錄與封鎖兩種模式。它是獨立專案、不隨本
仓库隨附。兩層彼此獨立——單獨运行皆有意義，同時運行則互補。
