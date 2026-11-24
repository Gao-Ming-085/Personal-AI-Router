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
