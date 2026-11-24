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

## Engine Strata

Strata là một định danh engine hạng nhất (`engines.Strata`). Facade phía
trước nó tuân theo quy tắc "một engine, một facade" như mọi engine khác,
nhưng quy hoạch cổng là riêng: facade Strataràng buộc cổng cơ sở **18080** và
engine-manager khởi động `strata serve` bằng hồ sơ khởi động loopback tại
**127.0.0.1:18081** — cố ý tránh cổng mặc định 8080 của llama.cpp để hai
engine cùng tồn tại trên một máy.


Hai hợp đồng riêng của Strata:

- **Hồ sơ khởi động không chứa khóa.** Probe sức khỏe của engine-manager
  không gửi header `Authorization`, nên hồ sơ Strata do PAIR quản lý bind
  vào loopback và bỏ qua `api_key`; xác thực nằm ở biên (client hoặc guard
  phía trên giữ khóa), và cổng thô của engine không bao giờ lộ ra ngoài máy.
- **Làm sạch phản hồi bật mặc định** — xem mục "Làm sạch phản hồi của
  Strata" ở trên.

Các máy Strata từ xa (và mọi engine tương thích OpenAI trên cổng không mặc
định) tham gia qua manual nodes với `lmstudio_port` và `lmstudio_api_key`
tùy chọn; xem [nvpair-manual-nodes](../nvpair-manual-nodes/README.md).

## Tùy chọn: guard phía client chống tool-call

Bộ làm sạch ở mục trên dọn markup mà engine đã tạo; một lớp bổ sung nằm ở
phía client, trước khi request vào router: một guard bên ngoài (ví dụ
`safeagents`) có thể từ chối lời gọi tool tới các tool chưa khai báo hoặc
host lạ trước khi chúng vào định tuyến, ở chế độ chỉ ghi log hoặc chặn. Đây
là dự án riêng, không đi kèm repo này. Hai lớp độc lập — mỗi lớp một mình
đều hữu ích và chúng bổ trợ cho nhau.
