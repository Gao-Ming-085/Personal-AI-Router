<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Engine Proxy (bản dịch tiếng Việt)

Tài liệu này là bản dịch một phần của [README.md](README.md) sang tiếng Việt,
hiện bao gồm mục dưới đây; các phần khác vui lòng tham bản tiếng Anh.

## Làm sạch phản hồi của Strata

Chat template UD của Strata có thể phát ra markup `<tool_call>` cho những
công cụ mà request không hề khai báo. Engine xem markup đó là một lời gọi bị
từ chối: nó không cắt bỏ gì, chỉ đánh dấu phản hồi bằng
`strata_rejected_tool_calls` trong phản hồi gốc, và — khi template không còn
nội dung nào khác để trả — trả chính markup đó làm nội dung câu trả lời.
Client bỏ qua cờ này sẽ hấp thụ lại các control token của chính mình như nội
dung hội thoại, tức vô tình dạy model cố tình làm rò rỉ chúng.

Khi engine được định tuyến bật tùy chọn
(`engineProfile.SanitizeToolCallLeaks`, chỉ áp dụng cho Strata), facade sẽ
kiểm tra phản hồi trên đường về — bao gồm cả request cục bộ lẫn liên máy,
tại cả listener phẳng cục bộ và ingress mTLS của cluster:

- **Không stream**: loại bỏ cờ; làm sạch các khối rò rỉ trong nội dung của
  từng choice; khi lời từ chối không để lại nội dung hợp lệ nào, một ghi chú
  ngắn thay thế cho markup.
- **Có stream**: các delta đi qua nguyên vẹn cho đến khi xuất hiện cờ hoặc
  phần mở đầu gợi ý của marker. Từ đó phần đuôi được giữ lại (một tiền tố
  marker chưa hoàn chỉnh không bao giờ được gửi), và khi stream kết thúc,
  phần giữ sẽ được đẩy ra dưới dạng đã làm sạch hoặc thay bằng ghi chú.
  Stream luôn kết thúc bằng `[DONE]`.
- Phản hồi **không có cờ** không bao giờ bị viết lại; mọi thứ không phân
  tích được đi qua nguyên vẹn từng byte; các route không phải suy luận không
  bị ảnh hưởng.

Bộ làm sạch không thử lại hay hỏi lại: nó chỉ loại bỏ markup điều khiển khỏi
những gì engine đã tạo. Hành vi phục hồi (hỏi lại model với chỉ dẫn sửa sai)
là trách nhiệm của client, không phải của proxy.
