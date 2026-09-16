/*
 * A small WebGL 1 liquid-glass pass for the DOM launcher.
 *
 * The reference project renders its whole catalog in WebGL.  CHUZI keeps its
 * business UI as semantic DOM, so this renderer owns only the optical layer:
 * one shared canvas renders the optical layer while each liquid surface keeps
 * its semantic DOM children above it.  The shader still performs the important parts of the
 * reference pipeline: low-frequency environment sampling, multi-tap blur,
 * edge refraction, chromatic channel offsets, rim light, inner shadow, and a
 * pointer/press highlight.  It deliberately samples a generated monochrome
 * environment rather than a desktop screenshot or a DOM capture.
 */

const VERTEX_SHADER = `
attribute vec2 aPosition;
void main() {
  gl_Position = vec4(aPosition, 0.0, 1.0);
}
`;

const BACKGROUND_FRAGMENT_SHADER = `
precision highp float;
uniform vec2 uViewport;
uniform float uTime;
uniform float uTheme;

float hash(vec2 p) {
  return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453123);
}

float noise(vec2 p) {
  vec2 i = floor(p);
  vec2 f = fract(p);
  f = f * f * (3.0 - 2.0 * f);
  return mix(
    mix(hash(i), hash(i + vec2(1.0, 0.0)), f.x),
    mix(hash(i + vec2(0.0, 1.0)), hash(i + vec2(1.0, 1.0)), f.x),
    f.y
  );
}

vec3 environmentColor(vec2 uv) {
  float n = noise(uv * 5.5 + vec2(uTime * 0.018, -uTime * 0.012));
  float diagonal = 0.5 + 0.5 * sin((uv.x * 1.15 + uv.y * 0.82) * 6.283 + uTime * 0.07);
  float warm = exp(-length(uv - vec2(0.16, 0.24)) * 5.0);
  float violet = exp(-length(uv - vec2(0.78, 0.26)) * 4.4);
  float cyan = exp(-length(uv - vec2(0.78, 0.82)) * 4.8);
  vec3 color = mix(vec3(0.90, 0.92, 0.98), vec3(0.035, 0.055, 0.16), uTheme);
  color += mix(vec3(0.18, 0.055, 0.018), vec3(0.08, 0.05, 0.22), uTheme) * warm;
  color += mix(vec3(0.08, 0.025, 0.14), vec3(0.18, 0.04, 0.20), uTheme) * violet;
  color += mix(vec3(0.02, 0.14, 0.18), vec3(0.02, 0.16, 0.22), uTheme) * cyan;
  color += vec3((n - 0.5) * 0.12 + (diagonal - 0.5) * 0.08);
  return clamp(color, 0.0, 1.0);
}

void main() {
  vec2 uv = vec2(gl_FragCoord.x, uViewport.y - gl_FragCoord.y) / max(uViewport, vec2(1.0));
  gl_FragColor = vec4(environmentColor(uv), 1.0);
}
`;

const FRAGMENT_SHADER = `
precision highp float;

uniform vec2 uSize;
uniform vec2 uOrigin;
uniform vec2 uViewport;
uniform float uTime;
uniform float uRadius;
uniform float uOpacity;
uniform float uBlur;
uniform float uRefraction;
uniform float uDispersion;
uniform float uPointerStrength;
uniform float uPressed;
uniform float uTheme;
uniform vec2 uPointer;

float sdRoundRect(vec2 p, vec2 halfSize, float radius) {
  vec2 q = abs(p) - halfSize + radius;
  return length(max(q, 0.0)) + min(max(q.x, q.y), 0.0) - radius;
}

float hash(vec2 p) {
  return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453123);
}

float noise(vec2 p) {
  vec2 i = floor(p);
  vec2 f = fract(p);
  f = f * f * (3.0 - 2.0 * f);
  return mix(
    mix(hash(i), hash(i + vec2(1.0, 0.0)), f.x),
    mix(hash(i + vec2(0.0, 1.0)), hash(i + vec2(1.0, 1.0)), f.x),
    f.y
  );
}

/* A quiet procedural wallpaper. Its coordinate is window-relative, so
   neighboring glass controls share a continuous backdrop like the reference
   renderer's scene texture. The palette is intentionally restrained so the
   launcher remains readable while dispersion can still separate channels. */
vec3 environment(vec2 localPx) {
  vec2 uv = (uOrigin + localPx) / max(uViewport, vec2(1.0));
  float n = noise(uv * 5.5 + vec2(uTime * 0.018, -uTime * 0.012));
  float diagonal = 0.5 + 0.5 * sin((uv.x * 1.15 + uv.y * 0.82) * 6.283 + uTime * 0.07);
  float warm = exp(-length(uv - vec2(0.16, 0.24)) * 5.0);
  float violet = exp(-length(uv - vec2(0.78, 0.26)) * 4.4);
  float cyan = exp(-length(uv - vec2(0.78, 0.82)) * 4.8);
  vec3 color = mix(vec3(0.90, 0.92, 0.98), vec3(0.035, 0.055, 0.16), uTheme);
  color += mix(vec3(0.18, 0.055, 0.018), vec3(0.08, 0.05, 0.22), uTheme) * warm;
  color += mix(vec3(0.08, 0.025, 0.14), vec3(0.18, 0.04, 0.20), uTheme) * violet;
  color += mix(vec3(0.02, 0.14, 0.18), vec3(0.02, 0.16, 0.22), uTheme) * cyan;
  color += vec3((n - 0.5) * 0.12 + (diagonal - 0.5) * 0.08);
  return clamp(color, 0.0, 1.0);
}

vec3 blurredEnvironment(vec2 p, float radius) {
  vec2 x = vec2(radius, 0.0);
  vec2 y = vec2(0.0, radius);
  vec2 d = vec2(radius * 0.7071);
  vec3 sum = environment(p) * 0.24;
  sum += environment(p + x) * 0.12;
  sum += environment(p - x) * 0.12;
  sum += environment(p + y) * 0.12;
  sum += environment(p - y) * 0.12;
  sum += environment(p + vec2(d.x, d.y)) * 0.07;
  sum += environment(p + vec2(-d.x, d.y)) * 0.07;
  sum += environment(p + vec2(d.x, -d.y)) * 0.07;
  sum += environment(p - vec2(d.x, d.y)) * 0.07;
  return sum;
}

void main() {
  vec2 screen = vec2(gl_FragCoord.x, uViewport.y - gl_FragCoord.y);
  vec2 p = screen - uOrigin;
  vec2 center = uSize * 0.5;
  vec2 halfSize = max(center - vec2(1.0), vec2(1.0));
  float radius = min(uRadius, min(halfSize.x, halfSize.y));
  float sd = sdRoundRect(p - center, halfSize, radius);
  // Keep the shader WebGL 1 compatible.  The reference uses derivative-based
  // edge AA in some passes, but the DOM canvas must also work on WebKitGTK
  // implementations that do not expose OES_standard_derivatives.
  float aa = 1.0;
  float coverage = 1.0 - smoothstep(0.0, aa, sd);
  if (coverage < 0.002) discard;

  float inside = max(-sd, 0.0);
  float edge = 1.0 - smoothstep(1.0, max(5.0, radius * 0.34), inside);
  vec2 e = vec2(1.25, 0.0);
  float dx = sdRoundRect(p - center + e, halfSize, radius) - sdRoundRect(p - center - e, halfSize, radius);
  float dy = sdRoundRect(p - center + e.yx, halfSize, radius) - sdRoundRect(p - center - e.yx, halfSize, radius);
  vec2 normal = normalize(vec2(dx, dy) + vec2(0.0001));

  vec2 pointerDelta = p - uPointer;
  float pointerGlow = 1.0 - smoothstep(0.0, max(uSize.x, uSize.y) * 0.72, length(pointerDelta));
  pointerGlow *= uPointerStrength;
  float press = uPressed;
  vec2 refract = normal * (edge * uRefraction) + normalize(pointerDelta + vec2(0.001)) * pointerGlow * 1.8;
  refract *= mix(1.0, 0.72, press);

  float blurRadius = max(0.0, uBlur + press * 1.5);
  vec3 base = blurredEnvironment(p + refract, blurRadius);
  vec3 red = blurredEnvironment(p + refract * (1.0 + uDispersion), blurRadius).rbb;
  vec3 blue = blurredEnvironment(p + refract * (1.0 - uDispersion), blurRadius).rrb;
  vec3 color = mix(base, vec3(red.r, base.g, blue.b), uDispersion);

  vec3 authored = vec3(1.0 - uTheme);
  color = mix(color, authored, 0.18 + press * 0.05);
  float light = clamp(dot(normal, normalize(vec2(-0.58, -0.82))), 0.0, 1.0);
  float rim = edge * (0.22 + light * 0.48) + pointerGlow * 0.17;
  float innerShadow = smoothstep(0.0, max(5.0, radius * 0.30), inside) * (1.0 - smoothstep(0.0, max(8.0, radius * 0.64), inside));
  color += vec3(rim * (1.0 - uTheme) * 0.34 + rim * uTheme * 0.18);
  color -= vec3(innerShadow * (0.065 + press * 0.04));
  color = clamp(color, 0.0, 1.0);

  float alpha = clamp(uOpacity + edge * 0.08 + pointerGlow * 0.035, 0.0, 0.98) * coverage;
  gl_FragColor = vec4(color, alpha);
}
`;

interface SurfaceState {
  surface: HTMLElement;
  width: number;
  height: number;
  dpr: number;
  pointerX: number;
  pointerY: number;
  pointerStrength: number;
  pointerTarget: number;
  pressed: boolean;
  pressProgress: number;
  radius: number;
  resizeObserver?: ResizeObserver;
}

const LIQUID_CONTROL_SELECTOR =
  "button, select, input[type='range'], .glass-toggle-track, .glass-tabs, .glass-progress, .glass-scroll-demo, .glass-lens-preview";

interface RendererState {
  canvas: HTMLCanvasElement;
  gl: WebGLRenderingContext;
  glassProgram: WebGLProgram;
  backgroundProgram: WebGLProgram;
  buffer: WebGLBuffer;
  glassLocations: Record<string, WebGLUniformLocation | null>;
  backgroundLocations: Record<string, WebGLUniformLocation | null>;
  width: number;
  height: number;
  dpr: number;
}

function compile(gl: WebGLRenderingContext, kind: number, source: string): WebGLShader {
  const shader = gl.createShader(kind);
  if (!shader) throw new Error("liquid glass shader allocation failed");
  gl.shaderSource(shader, source);
  gl.compileShader(shader);
  if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
    const log = gl.getShaderInfoLog(shader) ?? "unknown shader error";
    gl.deleteShader(shader);
    throw new Error(`liquid glass shader compilation failed: ${log}`);
  }
  return shader;
}

function createProgram(gl: WebGLRenderingContext): WebGLProgram {
  const vertex = compile(gl, gl.VERTEX_SHADER, VERTEX_SHADER);
  const fragment = compile(gl, gl.FRAGMENT_SHADER, FRAGMENT_SHADER);
  const program = gl.createProgram();
  if (!program) throw new Error("liquid glass program allocation failed");
  gl.attachShader(program, vertex);
  gl.attachShader(program, fragment);
  gl.linkProgram(program);
  gl.deleteShader(vertex);
  gl.deleteShader(fragment);
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
    const log = gl.getProgramInfoLog(program) ?? "unknown program error";
    gl.deleteProgram(program);
    throw new Error(`liquid glass program link failed: ${log}`);
  }
  return program;
}

function createBackgroundProgram(gl: WebGLRenderingContext): WebGLProgram {
  const vertex = compile(gl, gl.VERTEX_SHADER, VERTEX_SHADER);
  const fragment = compile(gl, gl.FRAGMENT_SHADER, BACKGROUND_FRAGMENT_SHADER);
  const program = gl.createProgram();
  if (!program) throw new Error("liquid glass background allocation failed");
  gl.attachShader(program, vertex);
  gl.attachShader(program, fragment);
  gl.linkProgram(program);
  gl.deleteShader(vertex);
  gl.deleteShader(fragment);
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
    const log = gl.getProgramInfoLog(program) ?? "unknown background program error";
    gl.deleteProgram(program);
    throw new Error(`liquid glass background link failed: ${log}`);
  }
  return program;
}

function numericRadius(surface: HTMLElement, width: number, height: number): number {
  const computed = getComputedStyle(surface);
  const value = Number.parseFloat(computed.borderTopLeftRadius);
  if (!Number.isFinite(value)) return Math.min(width, height) * 0.22;
  return Math.min(value >= 999 ? Math.min(width, height) / 2 : value, Math.min(width, height) / 2);
}

function webglFor(canvas: HTMLCanvasElement): WebGLRenderingContext | null {
  try {
    return (canvas.getContext("webgl", { alpha: true, antialias: true, premultipliedAlpha: false })
      ?? canvas.getContext("experimental-webgl", { alpha: true, antialias: true })) as WebGLRenderingContext | null;
  } catch {
    return null;
  }
}

function writeDataset(element: HTMLElement, key: string, value: string): void {
  if (element.dataset[key] !== value) element.dataset[key] = value;
}

export class LiquidGlassRenderer {
  private readonly surfaces = new Map<HTMLElement, SurfaceState>();
  private readonly observer: MutationObserver;
  private readonly renderer?: RendererState;
  private frame?: number;
  private animationFrame?: number;
  private lastTime = 0;
  private destroyed = false;
  private lastPointerSurface: HTMLElement | null = null;

  constructor() {
    this.renderer = this.createRenderer();
    this.observer = new MutationObserver(() => this.scheduleRefresh());
    this.observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["data-liquid-enhanced", "data-surface-resolved-material", "data-liquid-control", "hidden", "class", "open"] });
    document.addEventListener("pointermove", this.onPointerMove, { passive: true });
    document.addEventListener("pointerdown", this.onPointerDown, { passive: true });
    document.addEventListener("pointerup", this.onPointerUp, { passive: true });
    document.addEventListener("pointercancel", this.onPointerUp, { passive: true });
    window.addEventListener("resize", this.scheduleRefresh, { passive: true });
    window.addEventListener("scroll", this.scheduleRefresh, { passive: true, capture: true });
    window.addEventListener("chuzi:appearance-change", this.scheduleRefresh);
    this.refresh();
    this.tick();
  }

  private createRenderer(): RendererState | undefined {
    const canvas = document.createElement("canvas");
    canvas.className = "liquid-glass-canvas";
    canvas.setAttribute("aria-hidden", "true");
    const gl = webglFor(canvas);
    if (!gl) return undefined;
    let glassProgram: WebGLProgram | undefined;
    let backgroundProgram: WebGLProgram | undefined;
    let buffer: WebGLBuffer | undefined;
    try {
      glassProgram = createProgram(gl);
      backgroundProgram = createBackgroundProgram(gl);
      buffer = gl.createBuffer() ?? undefined;
      if (!buffer) throw new Error("liquid glass background buffer allocation failed");
      if (!glassProgram || !backgroundProgram) throw new Error("liquid glass program setup failed");
      const compiledGlassProgram = glassProgram;
      const compiledBackgroundProgram = backgroundProgram;
      gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
      gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
      const glassNames = ["uSize", "uOrigin", "uViewport", "uTime", "uRadius", "uOpacity", "uBlur", "uRefraction", "uDispersion", "uPointerStrength", "uPressed", "uTheme", "uPointer"];
      const glassLocations: Record<string, WebGLUniformLocation | null> = {};
      glassNames.forEach((name) => { glassLocations[name] = gl.getUniformLocation(compiledGlassProgram, name); });
      const backgroundLocations: Record<string, WebGLUniformLocation | null> = {};
      ["uViewport", "uTime", "uTheme"].forEach((name) => { backgroundLocations[name] = gl.getUniformLocation(compiledBackgroundProgram, name); });
      document.body.prepend(canvas);
      gl.clearColor(0, 0, 0, 0);
      return {
        canvas,
        gl,
        glassProgram: compiledGlassProgram,
        backgroundProgram: compiledBackgroundProgram,
        buffer,
        glassLocations,
        backgroundLocations,
        width: 0,
        height: 0,
        dpr: 1
      };
    } catch {
      if (buffer) gl.deleteBuffer(buffer);
      if (glassProgram) gl.deleteProgram(glassProgram);
      if (backgroundProgram) gl.deleteProgram(backgroundProgram);
      canvas.remove();
      return undefined;
    }
  }

  private readonly scheduleRefresh = (): void => {
    if (this.destroyed) return;
    if (this.frame !== undefined) return;
    this.frame = window.requestAnimationFrame(() => {
      this.frame = undefined;
      this.refresh();
    });
  };

  private readonly onPointerMove = (event: PointerEvent): void => {
    const target = event.target;
    const surface = target instanceof Element ? target.closest<HTMLElement>("[data-liquid-enhanced='true'], [data-liquid-control='true']") : null;
    if (!surface) {
      if (this.lastPointerSurface) {
        const state = this.surfaces.get(this.lastPointerSurface);
        if (state) state.pointerTarget = 0;
      }
      this.lastPointerSurface = null;
      return;
    }
    const state = this.surfaces.get(surface);
    if (!state) return;
    if (this.lastPointerSurface && this.lastPointerSurface !== surface) {
      const previous = this.surfaces.get(this.lastPointerSurface);
      if (previous) previous.pointerTarget = 0;
    }
    const rect = surface.getBoundingClientRect();
    state.pointerX = (event.clientX - rect.left) * state.dpr;
    state.pointerY = (event.clientY - rect.top) * state.dpr;
    state.pointerTarget = 1;
    this.lastPointerSurface = surface;
  };

  private readonly onPointerDown = (event: PointerEvent): void => {
    const target = event.target;
    const surface = target instanceof Element ? target.closest<HTMLElement>("[data-liquid-enhanced='true'], [data-liquid-control='true']") : null;
    const state = surface ? this.surfaces.get(surface) : undefined;
    if (state) state.pressed = true;
  };

  private readonly onPointerUp = (): void => {
    this.surfaces.forEach((state) => { state.pressed = false; });
  };

  private refresh(): void {
    if (this.destroyed) return;
    const root = document.documentElement;
    const wanted = new Set<HTMLElement>();
    document.querySelectorAll<HTMLElement>(".material-surface").forEach((surface) => {
      const isChrome = surface.dataset.surfaceArea === "chrome";
      const requested = isChrome ? root.dataset.chromeMaterial : root.dataset.material;
      const resolved = isChrome ? root.dataset.chromeResolvedMaterial : root.dataset.resolvedMaterial;
      const enabled = requested === "liquid" && resolved === "liquid-basic" &&
        surface.dataset.surfaceReducedEffects !== "true" &&
        root.dataset.reducedMotion !== "true" &&
        root.dataset.contrastGuard !== "true";
      writeDataset(surface, "surfaceMaterial", requested ?? "solid");
      writeDataset(surface, "surfaceResolvedMaterial", resolved ?? "solid");
      writeDataset(surface, "liquidEnhanced", String(enabled));
      if (enabled) wanted.add(surface);
    });
    document.querySelectorAll<HTMLElement>(LIQUID_CONTROL_SELECTOR).forEach((control) => {
      const chromeAncestor = control.closest<HTMLElement>('[data-surface-area="chrome"]');
      const requested = chromeAncestor ? root.dataset.chromeMaterial : root.dataset.material;
      const resolved = chromeAncestor ? root.dataset.chromeResolvedMaterial : root.dataset.resolvedMaterial;
      const enabled = requested === "liquid" && resolved === "liquid-basic" &&
        root.dataset.reducedMotion !== "true" && root.dataset.contrastGuard !== "true";
      writeDataset(control, "liquidControl", String(enabled));
    });
    if (this.renderer) {
      document.querySelectorAll<HTMLElement>(
        "button[data-liquid-control='true'], select[data-liquid-control='true'], input[type='range'][data-liquid-control='true'], .glass-toggle-track[data-liquid-control='true'], .glass-tabs[data-liquid-control='true'], .glass-progress[data-liquid-control='true'], .glass-scroll-demo[data-liquid-control='true'], .glass-lens-preview[data-liquid-control='true']"
      ).forEach((control) => {
        if (control.isConnected && !control.hidden) wanted.add(control);
      });
    } else {
      wanted.clear();
    }
    this.surfaces.forEach((state, surface) => {
      if (wanted.has(surface) && !surface.hidden && surface.isConnected) return;
      state.resizeObserver?.disconnect();
      this.surfaces.delete(surface);
      surface.removeAttribute("data-liquid-renderer");
    });
    wanted.forEach((surface) => {
      if (surface.hidden || !surface.isConnected || this.surfaces.has(surface)) return;
      this.attach(surface);
    });
    const active = this.surfaces.size > 0 && Boolean(this.renderer);
    writeDataset(root, "liquidRenderer", active ? "webgl1" : "css");
    if (this.renderer) this.renderer.canvas.hidden = !active;
  }

  private attach(surface: HTMLElement): void {
    const state: SurfaceState = {
      surface,
      width: 0,
      height: 0,
      dpr: 1,
      pointerX: 0,
      pointerY: 0,
      pointerStrength: 0,
      pointerTarget: 0,
      pressed: false,
      pressProgress: 0,
      radius: 12
    };
    writeDataset(surface, "liquidRenderer", "webgl1");
    state.resizeObserver = typeof ResizeObserver === "function" ? new ResizeObserver(() => this.resize(state)) : undefined;
    state.resizeObserver?.observe(surface);
    this.surfaces.set(surface, state);
    this.resize(state);
  }

  private resize(state: SurfaceState): void {
    const rect = state.surface.getBoundingClientRect();
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    state.dpr = dpr;
    state.width = Math.max(1, rect.width);
    state.height = Math.max(1, rect.height);
    state.radius = numericRadius(state.surface, state.width, state.height);
  }

  private render(state: SurfaceState, time: number): void {
    const renderer = this.renderer;
    if (!renderer) return;
    const root = document.documentElement;
    const gl = renderer.gl;
    const dpr = renderer.dpr;
    const rect = state.surface.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0 || rect.bottom <= 0 || rect.right <= 0 || rect.left >= window.innerWidth || rect.top >= window.innerHeight) return;
    state.radius = numericRadius(state.surface, rect.width, rect.height);
    const viewportHeight = renderer.height;
    const x = Math.max(0, Math.floor(rect.left * dpr));
    const y = Math.max(0, Math.floor(viewportHeight - (rect.bottom * dpr)));
    const width = Math.min(renderer.width - x, Math.max(1, Math.ceil(rect.width * dpr)));
    const height = Math.min(renderer.height - y, Math.max(1, Math.ceil(rect.height * dpr)));
    if (width <= 0 || height <= 0) return;
    gl.scissor(x, y, width, height);
    gl.useProgram(renderer.glassProgram);
    gl.bindBuffer(gl.ARRAY_BUFFER, renderer.buffer);
    const position = gl.getAttribLocation(renderer.glassProgram, "aPosition");
    gl.enableVertexAttribArray(position);
    gl.vertexAttribPointer(position, 2, gl.FLOAT, false, 0, 0);
    const set2 = (name: string, a: number, b: number): void => gl.uniform2f(renderer.glassLocations[name], a, b);
    const set1 = (name: string, value: number): void => gl.uniform1f(renderer.glassLocations[name], value);
    set2("uSize", state.width * dpr, state.height * dpr);
    set2("uOrigin", rect.left * dpr, rect.top * dpr);
    set2("uViewport", renderer.width, renderer.height);
    set1("uTime", time * 0.001);
    set1("uRadius", state.radius * dpr);
    set1("uOpacity", Number.parseFloat(getComputedStyle(state.surface).getPropertyValue("--cz-opacity-liquid")) || 0.78);
    set1("uBlur", 8 * dpr);
    set1("uRefraction", 7 * dpr);
    const isChrome = Boolean(state.surface.closest<HTMLElement>('[data-surface-area="chrome"]'));
    const dispersion = isChrome ? root.dataset.chromeDispersion : root.dataset.dispersion;
    set1("uDispersion", dispersion === "true" ? 0.07 : 0.012);
    set1("uPointerStrength", state.pointerStrength);
    set1("uPressed", state.pressProgress);
    set1("uTheme", root.dataset.theme === "dark" ? 1 : 0);
    set2("uPointer", state.pointerStrength > 0 ? state.pointerX : state.width * dpr * 0.5, state.pointerStrength > 0 ? state.pointerY : state.height * dpr * 0.35);
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
  }

  private renderBackground(time: number): void {
    const renderer = this.renderer;
    if (!renderer) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const width = Math.max(1, Math.round(window.innerWidth * dpr));
    const height = Math.max(1, Math.round(window.innerHeight * dpr));
    if (renderer.width !== width || renderer.height !== height || renderer.dpr !== dpr) {
      renderer.width = width;
      renderer.height = height;
      renderer.dpr = dpr;
      renderer.canvas.width = width;
      renderer.canvas.height = height;
    }
    const gl = renderer.gl;
    gl.viewport(0, 0, width, height);
    gl.disable(gl.SCISSOR_TEST);
    gl.disable(gl.BLEND);
    gl.useProgram(renderer.backgroundProgram);
    gl.bindBuffer(gl.ARRAY_BUFFER, renderer.buffer);
    const position = gl.getAttribLocation(renderer.backgroundProgram, "aPosition");
    gl.enableVertexAttribArray(position);
    gl.vertexAttribPointer(position, 2, gl.FLOAT, false, 0, 0);
    gl.uniform2f(renderer.backgroundLocations.uViewport, width, height);
    gl.uniform1f(renderer.backgroundLocations.uTime, time * 0.001);
    gl.uniform1f(renderer.backgroundLocations.uTheme, document.documentElement.dataset.theme === "dark" ? 1 : 0);
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
    gl.enable(gl.SCISSOR_TEST);
  }

  private tick = (time = 0): void => {
    if (this.destroyed) return;
    const delta = this.lastTime > 0 ? Math.min(64, Math.max(0, time - this.lastTime)) : 16;
    this.lastTime = time;
    const pointerStep = 1 - Math.exp(-delta / 115);
    const pressStep = 1 - Math.exp(-delta / 72);
    if (this.surfaces.size > 0) this.renderBackground(time);
    this.surfaces.forEach((state) => {
      state.pointerStrength += (state.pointerTarget - state.pointerStrength) * pointerStep;
      state.pressProgress += ((state.pressed ? 1 : 0) - state.pressProgress) * pressStep;
      this.render(state, time);
    });
    this.animationFrame = window.requestAnimationFrame(this.tick);
  };

  dispose(): void {
    this.destroyed = true;
    this.observer.disconnect();
    if (this.frame !== undefined) window.cancelAnimationFrame(this.frame);
    if (this.animationFrame !== undefined) window.cancelAnimationFrame(this.animationFrame);
    document.removeEventListener("pointermove", this.onPointerMove);
    document.removeEventListener("pointerdown", this.onPointerDown);
    document.removeEventListener("pointerup", this.onPointerUp);
    document.removeEventListener("pointercancel", this.onPointerUp);
    window.removeEventListener("resize", this.scheduleRefresh);
    window.removeEventListener("scroll", this.scheduleRefresh, true);
    window.removeEventListener("chuzi:appearance-change", this.scheduleRefresh);
    this.surfaces.forEach((state) => {
      state.resizeObserver?.disconnect();
      state.surface.removeAttribute("data-liquid-renderer");
    });
    this.surfaces.clear();
    if (this.renderer) {
      this.renderer.gl.deleteBuffer(this.renderer.buffer);
      this.renderer.gl.deleteProgram(this.renderer.glassProgram);
      this.renderer.gl.deleteProgram(this.renderer.backgroundProgram);
      this.renderer.canvas.remove();
    }
    document.documentElement.removeAttribute("data-liquid-renderer");
  }
}

let activeRenderer: LiquidGlassRenderer | undefined;

export function installLiquidGlassRenderer(): LiquidGlassRenderer | undefined {
  if (activeRenderer) return activeRenderer;
  if (typeof window === "undefined" || typeof document === "undefined" || !document.body) return undefined;
  activeRenderer = new LiquidGlassRenderer();
  return activeRenderer;
}

export function disposeLiquidGlassRenderer(): void {
  activeRenderer?.dispose();
  activeRenderer = undefined;
}
