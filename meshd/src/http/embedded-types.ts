// 静态资源条目类型。embedded.ts 会被 embed-web 脚本生成，引用这个类型。
//
// 选择 base64 字面量（不是 Uint8Array bytes）是为了让 bun --compile 把
// 模块打成单二进制时字面量数据不被切割；运行时一次性解码再缓存。

export interface EmbeddedFile {
  /** base64 编码的文件内容 */
  body_b64: string
  /** Content-Type header */
  content_type: string
}

export type EmbeddedAssets = Map<string, EmbeddedFile>
