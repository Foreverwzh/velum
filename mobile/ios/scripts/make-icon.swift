// 由桌面端图标生成 iOS 图标：iOS 要求不透明的方形（圆角由系统裁切），透明部分会显示成黑色。
// 裁掉四周的透明边距，圆角外用同一渐变填满。用法：swift scripts/make-icon.swift <源 png> <输出 png>
import AppKit

let args = CommandLine.arguments
let rep = NSBitmapImageRep(data: try! Data(contentsOf: URL(fileURLWithPath: args[1])))!
let image = rep.cgImage!
let size = 1024, inset = 64.0, kept = 1024.0 - 2 * inset // 不透明区域是 64...960
let space = image.colorSpace ?? CGColorSpace(name: CGColorSpace.sRGB)! // 沿用源图的色彩空间，颜色不变

/// 读源图的原始像素，不做色彩转换。
func raw(_ x: Int, _ y: Int) -> [Double] {
    var p = [Int](repeating: 0, count: rep.samplesPerPixel)
    rep.getPixel(&p, atX: x, y: y)
    return p.prefix(3).map { Double($0) / 255 }
}

// 背景是线性渐变：用三个不含图形的点解出平面 color = a + b·x + c·y（x、y 为源图坐标）
let p = [(300.0, 110.0), (110.0, 300.0), (800.0, 880.0)]
let v = p.map { raw(Int($0.0), Int($0.1)) }
func plane(_ i: Int, _ x: Double, _ y: Double) -> Double {
    let (x0, y0) = p[0], (x1, y1) = p[1], (x2, y2) = p[2]
    let det = (x1 - x0) * (y2 - y0) - (x2 - x0) * (y1 - y0)
    let b = ((v[1][i] - v[0][i]) * (y2 - y0) - (v[2][i] - v[0][i]) * (y1 - y0)) / det
    let c = ((x1 - x0) * (v[2][i] - v[0][i]) - (x2 - x0) * (v[1][i] - v[0][i])) / det
    return max(0, min(1, v[0][i] + b * (x - x0) + c * (y - y0)))
}

let ctx = CGContext(data: nil, width: size, height: size, bitsPerComponent: 8, bytesPerRow: size * 4,
                    space: space, bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
let buf = ctx.data!.assumingMemoryBound(to: UInt8.self)
let k = kept / Double(size)
for y in 0..<size {
    for x in 0..<size {
        let sx = inset + Double(x) * k, sy = inset + Double(y) * k
        for i in 0..<3 { buf[(y * size + x) * 4 + i] = UInt8(plane(i, sx, sy) * 255 + 0.5) }
    }
}
// 上面按内存顺序（第 0 行在最上面）填背景；再把源图放大叠上去，边缘由系统插值
ctx.interpolationQuality = .high
let scale = Double(size) / kept
ctx.draw(image, in: CGRect(x: -inset * scale, y: -inset * scale, width: 1024 * scale, height: 1024 * scale))
let out = NSBitmapImageRep(cgImage: ctx.makeImage()!).representation(using: .png, properties: [:])!
try! out.write(to: URL(fileURLWithPath: args[2]))
