# fda-tools-go License

The `fda-tools-go` project uses a split-license model. 
The original code of this project is licensed under the MIT License. However, the compiled binary and specific components are subject to third-party licenses, including a strict **NON-COMMERCIAL USE** restriction for the FFT algorithm.

See the sections below for details.

---

## 1. Original fda-tools-go Code (MIT License)

Copyright (c) 2026 Глеб Стручев

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

---

## 2. Third-Party Components and Exceptions

### A. mixfft.c (Jens Jorgen Nielsen) — NON-COMMERCIAL USE ONLY
The file `fft.go` is a direct Go translation of `mixfft.c` v1. 
Due to this dependency, the compiled binary of `fda-tools-go` and any use of the `fft.go` source code are restricted to **non-commercial purposes only**.

Original Copyright and Terms:
> Author: Jens Joergen Nielsen
> Bakkehusene 54, DK-2970 Hoersholm, DENMARK
> E-mail : jjn@get2net.dk
> All rights reserved. October 2000.
> 
> **For non-commercial use only.**
> **A $100 fee must be paid if used commercially. Please contact the original author.**

### B. Relic Codec Implementation (vgmstream)
The Relic Codec logic (`relic.go`, `relic_enc.go`) is derived and translated from the `vgmstream` project, which is licensed under the ISC License.

Original vgmstream Copyright and License:
> Copyright (c) 2008-2025 Adam Gashlin, Fastelbja, Ronny Elfert, bnnm,
>                         Christopher Snowhill, NicknineTheEagle, bxaimc,
>                         Thealexbarney, CyberBotX, EdnessP, et al
> 
> Portions Copyright (c) 2004-2008, Marko Kreen
> Portions Copyright 2001-2007  jagarl / Kazunori Ueno <jagarl@creator.club.ne.jp>
> Portions Copyright (c) 1998, Justin Frankel/Nullsoft Inc.
> Portions Copyright (C) 2006 Nullsoft, Inc.
> Portions Copyright (c) 2005-2007 Paul Hsieh
> Portions Copyright (C) 2000-2004 Leshade Entis, Entis-soft.
> Portions Public Domain originating with Sun Microsystems
> 
> Permission to use, copy, modify, and distribute this software for any
> purpose with or without fee is hereby granted, provided that the above
> copyright notice and this permission notice appear in all copies.
> 
> THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
> WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
> MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
> ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
> WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
> ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
> OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.