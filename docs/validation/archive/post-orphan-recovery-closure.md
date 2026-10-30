# post-orphan-recovery-closure：原始证据归档

> 本页是归档导航，不是新的验收结论。归档中的 Markdown 是整理前原文；公开阅读版报告可能增加了历史说明并更新证据链接。

[返回历史验收索引](../README.md) · [下载完整归档](post-orphan-recovery-closure.tar.gz) · [机器可读校验清单](manifest.json)

归档大小：114,275 字节；原始文件：22 个。

SHA-256：`04d0d1d005e50b109d9f3c65b9c8e7a7881a0b847f03f88d96af16fea7c72560`

从仓库根目录解包到独立目录，原目录层级会保留：

```sh
mkdir -p /tmp/devbridge-validation
tar -xzf docs/validation/archive/post-orphan-recovery-closure.tar.gz -C /tmp/devbridge-validation
```

原始日志、快照、补丁和当时的辅助脚本均按原字节保存。历史脚本可能包含旧路径和环境假设，仅用于追溯；当前验证入口见 [README](../../../README.md#开发与测试)。归档中的旧 SHA、源码身份和验收结果仍然只描述原批次。

| 原始文件 | 字节数 | SHA-256 |
| --- | ---: | --- |
| <a id="file-73ff8f12991282cf"></a>`acceptance-parity.log` | 2,714 | `6cd57e71235f6ae0143a70d7fe678a750610a820cffe82ed71555b0fd689de4a` |
| <a id="file-4ae9342c29f0f330"></a>`baseline.txt` | 1,187 | `30cd1ed7cfeddcdcb45988b106cb105c4e6a8de94d8ac5c26c72d5f0b2976695` |
| <a id="file-9c71578ece8267c9"></a>`canonical-doc-audit.txt` | 8,158 | `722fe0de28a8648fb2cbd69f2ea9474ded79d32a166699f665943c62f58d2fc2` |
| <a id="file-72fa8ec9d8b42170"></a>`desktop-config-regression.log` | 5,143 | `367b51d8a60b7a8691630611adfc74c5c1c65398aae1ac2c655539d9535de13e` |
| <a id="file-19adf361099e0a9c"></a>`diagnostics/initial-sandbox-go-tests.log` | 317,025 | `549ea05ad048837bc5441a78f935f2a8b500efdfad95154334a7dd2156842ed9` |
| <a id="file-88e58ae554a1bcf6"></a>`diff-check.txt` | 245 | `6910e8dbb3088e5092b7a84cdb7fda5be993465d55eee36e5b670633e712c9ad` |
| <a id="file-c95ef8e79fa3f9b5"></a>`final-git-status.txt` | 1,120 | `3de53be749a33b83e4b5283d808065ef44c599c020a4f4f28bd8eb89d59656d2` |
| <a id="file-6a9aee165b18a96b"></a>`final-report.md` | 13,645 | `71e17d6c7008781265a4ed87a24dc294a269e9e3da846e946c49ffe45c99ebd2` |
| <a id="file-8eede4f07c50075d"></a>`focused-recovery-acceptance.log` | 7,422 | `a2487009b0a36e1cb5213d3dab10534537f20186de853ac4dde0245d43d98af2` |
| <a id="file-ef488547204a8d59"></a>`git-delivery.log` | 3,660 | `b943ff0a3ece70388eda848b3f7aa61a3680ed7a94452e7feda1aecaecb72cfe` |
| <a id="file-48d331f79b055895"></a>`go-tests.log` | 312,491 | `fe80a097c14a50e85e4885ab33925e752a9e870c1ec999889174fd277f4f434b` |
| <a id="file-103914e637a90fc8"></a>`go-vet.txt` | 573 | `6bd0fbc2460a228cf56400bcaa5719fed070d53e44ef5fc4dd49c01d31b420e9` |
| <a id="file-4eb036b801ec0f71"></a>`migration-tests.log` | 6,930 | `074cfd2cd7c5e6ab20684a7e111eabbd00dbc48e24737773a249923858319f55` |
| <a id="file-30db3179bab782dd"></a>`modified-files.txt` | 2,458 | `b5977e8a2783fac16621781a9e4a2fcc732ea24fc0f0ce082acf4b46bde792e1` |
| <a id="file-3ff1cf811edb7ecf"></a>`node-tests.log` | 42,866 | `bcf09a7d4c797b9abef2bba8678eb94439294faaf22ecc23175e968fcca126a9` |
| <a id="file-e1409af14e7a4bf0"></a>`prompt-byte-identity.log` | 9,606 | `679155cfe2df18ccef53b507184284bd5bbd61b175d1f0b19461b1d2fada58bd` |
| <a id="file-19dee3c1681fd9f5"></a>`race-tests.log` | 9,393 | `08b3e8a46fd77e2b1f27c0c244abd0127b6d1b9855490d7f5eae1bfe214c93df` |
| <a id="file-16feadb3506c692f"></a>`runtime-config-regression.log` | 8,528 | `db1204e479c1f74a57267ebf785ba183bc5020ef699327c865d45b5144ac201e` |
| <a id="file-aa7da620209d7afa"></a>`static-audit.txt` | 108,587 | `5d84792be9c8d14310fb9f9f73d94005d3576fc2e66d90e28876093f1a4e7518` |
| <a id="file-612d06b9769cde10"></a>`test-summary.json` | 6,121 | `91b312ad4b8be565c9141882cb46bfafceb4d9ef38cca2a77f6262e61c8b295e` |
| <a id="file-7ac1b50666f5ce8b"></a>`tested-source-sha256.json` | 1,567 | `3ffc1c96bfd04132294b22cabe112abada24ffe47180fcef2f2b1ead9f9b58ff` |
| <a id="file-e677f5b59761b33d"></a>`validation-manifest.md` | 4,067 | `f5095e6a2051418e9571e7589f983c7004b27449047719ca00a3caf875489388` |
