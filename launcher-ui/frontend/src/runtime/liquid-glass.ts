/*
 * Liquid Glass optical layer for the semantic launcher DOM.
 *
 * The reference project (martin65536/liquid-glass-webgl, Apache-2.0, based on
 * Kyant0/AndroidLiquidGlass) owns the complete scene in WebGL. CHUZI keeps
 * business content and accessibility in DOM, so this renderer owns only the
 * optical pass. CSS remains the material fallback and the canvas is a
 * pointer-transparent overlay. That boundary prevents a lost WebGL context
 * from turning the launcher into a transparent hole.
 */

import { LIQUID_CONTROL_SELECTOR } from "./material.js";

const VERTEX_SHADER = `
attribute vec2 aPosition;
void main() {
  gl_Position = vec4(aPosition, 0.0, 1.0);
}
`;

/*
 * This is the DOM adapter's equivalent of the reference element pass:
 * rounded shape -> low-frequency backdrop taps -> edge refraction -> optional
 * chromatic offsets -> rim/highlight/press overlay. WebGL 1 keeps the tap
 * count static, so the Vogel-style 16 taps are explicit. The backdrop sampler
 * is optional: a native transparent window remains useful with CSS fallback,
 * while an injected image/canvas can provide the reference renderer's scene.
 */
const FRAGMENT_SHADER = `
precision highp float;

uniform vec2 uSize;
uniform vec2 uOrigin;
uniform vec2 uViewport;
uniform vec2 uPointer;
uniform float uRadius;
uniform float uOpacity;
uniform float uBlur;
uniform float uRefraction;
uniform float uDispersion;
uniform float uPointerStrength;
uniform float uPressed;
uniform float uTheme;
uniform sampler2D uBackdrop;
uniform float uHasBackdrop;

float sdRoundRect(vec2 p, vec2 halfSize, float radius) {
  vec2 q = abs(p) - halfSize + radius;
  return length(max(q, 0.0)) + min(max(q.x, q.y), 0.0) - radius;
}

/* A neutral fallback keeps the optical pass transparent in spirit. The actual
   page/desktop backdrop stays under the DOM surface and is handled by CSS. */
vec3 environment(vec2 localPx) {
  vec2 uv = (uOrigin + localPx) / max(uViewport, vec2(1.0));
  vec3 neutral = vec3(mix(0.82, 0.18, uTheme));
  vec3 backdrop = texture2D(uBackdrop, vec2(uv.x, 1.0 - uv.y)).rgb;
  return mix(neutral, backdrop, uHasBackdrop);
}

vec3 blurredEnvironment(vec2 p, float radius) {
  /* 16 Vogel-disc taps. The weights sum to one and remain constant in WebGL1. */
  vec3 sum = environment(p) * 0.115;
  sum += environment(p + vec2(0.130, 0.142) * radius) * 0.045;
  sum += environment(p + vec2(-0.245, -0.224) * radius) * 0.082;
  sum += environment(p + vec2(0.044, 0.320) * radius) * 0.078;
  sum += environment(p + vec2(0.365, -0.160) * radius) * 0.074;
  sum += environment(p + vec2(-0.470, 0.168) * radius) * 0.070;
  sum += environment(p + vec2(0.447, 0.264) * radius) * 0.067;
  sum += environment(p + vec2(-0.112, -0.592) * radius) * 0.064;
  sum += environment(p + vec2(-0.292, 0.545) * radius) * 0.061;
  sum += environment(p + vec2(0.678, -0.246) * radius) * 0.058;
  sum += environment(p + vec2(-0.652, -0.352) * radius) * 0.055;
  sum += environment(p + vec2(0.226, 0.754) * radius) * 0.052;
  sum += environment(p + vec2(-0.604, 0.602) * radius) * 0.049;
  sum += environment(p + vec2(0.802, 0.398) * radius) * 0.046;
  sum += environment(p + vec2(-0.832, -0.082) * radius) * 0.043;
  sum += environment(p + vec2(0.488, -0.724) * radius) * 0.041;
  return sum;
}

void main() {
  vec2 screen = vec2(gl_FragCoord.x, uViewport.y - gl_FragCoord.y);
  vec2 p = screen - uOrigin;
  vec2 center = uSize * 0.5;
  vec2 halfSize = max(center - vec2(1.0), vec2(1.0));
  float radius = min(uRadius, min(halfSize.x, halfSize.y));
  float sd = sdRoundRect(p - center, halfSize, radius);
  float coverage = 1.0 - smoothstep(0.0, 1.15, sd);
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

  /* Reference defaults: blur ~= 2dp, refraction height ~= 12dp. */
  vec2 refracted = normal * (edge * uRefraction);
  refracted += normalize(pointerDelta + vec2(0.001)) * pointerGlow * 2.0;
  refracted *= mix(1.0, 0.72, press);
  float blurRadius = max(0.0, uBlur + press * 1.5);

  vec3 baseSample = blurredEnvironment(p + refracted, blurRadius);
  vec3 redSample = blurredEnvironment(p + refracted * (1.0 + uDispersion), blurRadius);
  vec3 blueSample = blurredEnvironment(p + refracted * (1.0 - uDispersion), blurRadius);
  vec3 environmentColor = vec3(redSample.r, baseSample.g, blueSample.b);
  vec3 authored = vec3(1.0 - uTheme);
  vec3 color = mix(environmentColor, authored, 0.18 + press * 0.05);

  float light = clamp(dot(normal, normalize(vec2(-0.58, -0.82))), 0.0, 1.0);
  float rim = edge * (0.22 + light * 0.48) + pointerGlow * 0.17;
  float innerShadow = smoothstep(0.0, max(5.0, radius * 0.30), inside) *
    (1.0 - smoothstep(0.0, max(8.0, radius * 0.64), inside));
  color += vec3(rim * (1.0 - uTheme) * 0.34 + rim * uTheme * 0.18);
  color -= vec3(innerShadow * (0.065 + press * 0.04));
  color = clamp(color, 0.0, 1.0);

  /* Keep the optical pass translucent so CSS/host backdrop remains the base.
     Without an injected scene there is no pixel source to refract; CSS remains
     the complete fallback instead of receiving a flat procedural wash. */
  float alpha = clamp(uOpacity + edge * 0.08 + pointerGlow * 0.035, 0.0, 0.46) * coverage * uHasBackdrop;
  gl_FragColor = vec4(color * alpha, alpha);
}
`;

interface SurfaceState {
  surface: HTMLElement;
  width: number;
  height: number;
  dpr: number;
  radius: number;
  pointerX: number;
  pointerY: number;
  pointerStrength: number;
  pointerTarget: number;
  pressed: boolean;
  pressProgress: number;
  resizeObserver?: ResizeObserver;
}

interface RendererState {
  canvas: HTMLCanvasElement;
  gl: WebGLRenderingContext;
  program: WebGLProgram;
  buffer: WebGLBuffer;
  backdropTexture: WebGLTexture;
  locations: Record<string, WebGLUniformLocation | null>;
  width: number;
  height: number;
  dpr: number;
  hasBackdrop: boolean;
}

/**
 * A scene source for the WebGL pass. DOM nodes and transparent desktop
 * compositors cannot be sampled directly by WebGL, so callers that own a
 * wallpaper, canvas, image, or video may inject it explicitly.
 */
export type LiquidGlassEnvironmentSource = TexImageSource;

let configuredEnvironmentSource: LiquidGlassEnvironmentSource | null = null;

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

function numericRadius(surface: HTMLElement, width: number, height: number): number {
  const computed = getComputedStyle(surface);
  const value = Number.parseFloat(computed.borderTopLeftRadius);
  if (!Number.isFinite(value)) return Math.min(width, height) * 0.22;
  return Math.min(value >= 999 ? Math.min(width, height) / 2 : value, Math.min(width, height) / 2);
}

function cssNumber(computed: CSSStyleDeclaration, property: string, fallback: number): number {
  const value = Number.parseFloat(computed.getPropertyValue(property));
  return Number.isFinite(value) ? value : fallback;
}

function createContext(canvas: HTMLCanvasElement): WebGLRenderingContext | null {
  try {
    return (canvas.getContext("webgl", {
      alpha: true,
      antialias: false,
      premultipliedAlpha: true,
      powerPreference: "low-power"
    }) ?? canvas.getContext("experimental-webgl", { alpha: true, antialias: false })) as WebGLRenderingContext | null;
  } catch {
    return null;
  }
}

function isVisible(surface: HTMLElement): boolean {
  if (!surface.isConnected || surface.hidden) return false;
  const rect = surface.getBoundingClientRect();
  return rect.width > 0 && rect.height > 0 &&
    rect.bottom >= -120 && rect.right >= -120 &&
    rect.left <= window.innerWidth + 120 && rect.top <= window.innerHeight + 120;
}

export class LiquidGlassRenderer {
  private readonly surfaces = new Map<HTMLElement, SurfaceState>();
  private readonly observer: MutationObserver;
  private renderer?: RendererState;
  private lostCanvas?: HTMLCanvasElement;
  private environmentSource: LiquidGlassEnvironmentSource | null = configuredEnvironmentSource;
  private environmentDirty = true;
  private environmentReadyTarget?: { target: EventTarget; events: readonly string[] };
  private refreshFrame?: number;
  private animationFrame?: number;
  private lastTime = 0;
  private dirty = true;
  private destroyed = false;
  private lastPointerSurface: HTMLElement | null = null;

  constructor() {
    this.renderer = this.createRenderer();
    this.bindEnvironmentSource(this.environmentSource);
    this.observer = new MutationObserver(() => this.scheduleRefresh());
    this.observer.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ["data-liquid-enhanced", "data-liquid-control", "data-surface-resolved-material", "hidden", "class", "open"]
    });
    document.addEventListener("pointermove", this.onPointerMove, { passive: true });
    document.addEventListener("pointerdown", this.onPointerDown, { passive: true });
    document.addEventListener("pointerup", this.onPointerUp, { passive: true });
    document.addEventListener("pointercancel", this.onPointerUp, { passive: true });
    window.addEventListener("resize", this.scheduleRefresh, { passive: true });
    window.addEventListener("scroll", this.scheduleRefresh, { passive: true, capture: true });
    window.addEventListener("chuzi:appearance-change", this.scheduleRefresh);
    this.refresh();
    this.ensureFrame();
  }

  private createRenderer(existingCanvas?: HTMLCanvasElement): RendererState | undefined {
    const ownsCanvas = !existingCanvas;
    const canvas = existingCanvas ?? document.createElement("canvas");
    canvas.className = "liquid-glass-canvas";
    canvas.setAttribute("aria-hidden", "true");
    const gl = createContext(canvas);
    if (!gl) return undefined;
    let program: WebGLProgram | undefined;
    let buffer: WebGLBuffer | undefined;
    let backdropTexture: WebGLTexture | undefined;
    try {
      program = createProgram(gl);
      buffer = gl.createBuffer() ?? undefined;
      if (!buffer) throw new Error("liquid glass buffer allocation failed");
      backdropTexture = gl.createTexture() ?? undefined;
      if (!backdropTexture) throw new Error("liquid glass backdrop texture allocation failed");
      const compiledProgram = program;
      const texture = backdropTexture;
      gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
      gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
      const locations: Record<string, WebGLUniformLocation | null> = {};
      ["uSize", "uOrigin", "uViewport", "uPointer", "uRadius", "uOpacity", "uBlur", "uRefraction", "uDispersion", "uPointerStrength", "uPressed", "uTheme", "uBackdrop", "uHasBackdrop"]
        .forEach((name) => { locations[name] = gl.getUniformLocation(compiledProgram, name); });
      gl.bindTexture(gl.TEXTURE_2D, texture);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      gl.texImage2D(
        gl.TEXTURE_2D,
        0,
        gl.RGBA,
        1,
        1,
        0,
        gl.RGBA,
        gl.UNSIGNED_BYTE,
        new Uint8Array([128, 128, 128, 255])
      );
      if (ownsCanvas) {
        canvas.addEventListener("webglcontextlost", this.onContextLost, false);
        canvas.addEventListener("webglcontextrestored", this.onContextRestored, false);
        document.body.prepend(canvas);
      }
      canvas.hidden = false;
      gl.clearColor(0, 0, 0, 0);
      return {
        canvas,
        gl,
        program: compiledProgram,
        buffer,
        backdropTexture: texture,
        locations,
        width: 0,
        height: 0,
        dpr: 1,
        hasBackdrop: false
      };
    } catch {
      if (backdropTexture) gl.deleteTexture(backdropTexture);
      if (buffer) gl.deleteBuffer(buffer);
      if (program) gl.deleteProgram(program);
      if (ownsCanvas) canvas.remove();
      return undefined;
    }
  }

  /** Replace the scene source without coupling the renderer to a DOM capture. */
  setEnvironmentSource(source: LiquidGlassEnvironmentSource | null): void {
    this.unbindEnvironmentSource();
    this.environmentSource = source;
    this.bindEnvironmentSource(source);
    this.environmentDirty = true;
    this.dirty = true;
    this.scheduleRefresh();
    this.ensureFrame();
  }

  private readonly onEnvironmentSourceReady = (): void => {
    if (this.destroyed) return;
    this.environmentDirty = true;
    this.dirty = true;
    this.ensureFrame();
  };

  private bindEnvironmentSource(source: LiquidGlassEnvironmentSource | null): void {
    if (!source) return;
    let target: EventTarget | undefined;
    let events: readonly string[] = [];
    if (typeof HTMLImageElement !== "undefined" && source instanceof HTMLImageElement) {
      target = source;
      events = ["load"];
    } else if (typeof HTMLVideoElement !== "undefined" && source instanceof HTMLVideoElement) {
      target = source;
      events = ["loadeddata", "canplay"];
    }
    if (!target) return;
    events.forEach((event) => target?.addEventListener(event, this.onEnvironmentSourceReady));
    this.environmentReadyTarget = { target, events };
  }

  private unbindEnvironmentSource(): void {
    const binding = this.environmentReadyTarget;
    if (!binding) return;
    binding.events.forEach((event) => binding.target.removeEventListener(event, this.onEnvironmentSourceReady));
    this.environmentReadyTarget = undefined;
  }

  private uploadEnvironment(renderer: RendererState): void {
    if (!this.environmentDirty) return;
    this.environmentDirty = false;
    renderer.hasBackdrop = false;
    const source = this.environmentSource;
    if (!source) return;
    const gl = renderer.gl;
    try {
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, renderer.backdropTexture);
      gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, 1);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, source);
      renderer.hasBackdrop = true;
    } catch {
      // A source can become unavailable while an image/video is loading. Keep
      // the CSS fallback; the source-ready listener or a later setter call
      // marks the texture dirty and retries the upload.
      renderer.hasBackdrop = false;
    }
  }

  private readonly onContextLost = (event: Event): void => {
    event.preventDefault();
    // Keep the canvas in the document. Removing it prevents the browser from
    // dispatching `webglcontextrestored`, leaving the CSS fallback permanent.
    this.lostCanvas = event.currentTarget instanceof HTMLCanvasElement
      ? event.currentTarget
      : this.renderer?.canvas;
    if (this.lostCanvas) this.lostCanvas.hidden = true;
    this.renderer = undefined;
    this.surfaces.forEach((state) => state.surface.removeAttribute("data-liquid-renderer"));
    document.documentElement.dataset.liquidRenderer = "css";
    this.dirty = false;
  };

  private readonly onContextRestored = (): void => {
    if (this.destroyed || this.renderer) return;
    this.environmentDirty = true;
    const canvas = this.lostCanvas;
    this.renderer = canvas ? this.createRenderer(canvas) : this.createRenderer();
    if (this.renderer) this.lostCanvas = undefined;
    this.refresh();
    this.ensureFrame();
  };

  private readonly scheduleRefresh = (): void => {
    if (this.destroyed || this.refreshFrame !== undefined) return;
    this.refreshFrame = window.requestAnimationFrame(() => {
      this.refreshFrame = undefined;
      this.refresh();
      this.ensureFrame();
    });
  };

  private eventSurface(target: EventTarget | null): HTMLElement | null {
    let element: HTMLElement | null = target instanceof Element ? target as HTMLElement : null;
    while (element) {
      if (this.surfaces.has(element)) return element;
      element = element.parentElement;
    }
    return null;
  }

  private readonly onPointerMove = (event: PointerEvent): void => {
    if (event.pointerType && event.pointerType !== "mouse" && event.pointerType !== "pen") return;
    const surface = this.eventSurface(event.target);
    if (!surface) {
      if (this.lastPointerSurface) {
        const previous = this.surfaces.get(this.lastPointerSurface);
        if (previous) previous.pointerTarget = 0;
      }
      this.lastPointerSurface = null;
      this.ensureFrame();
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
    this.ensureFrame();
  };

  private readonly onPointerDown = (event: PointerEvent): void => {
    const surface = this.eventSurface(event.target);
    const state = surface ? this.surfaces.get(surface) : undefined;
    if (state) {
      state.pressed = true;
      this.ensureFrame();
    }
  };

  private readonly onPointerUp = (): void => {
    this.surfaces.forEach((state) => { state.pressed = false; });
    this.ensureFrame();
  };

  private refresh(): void {
    if (this.destroyed) return;
    const wanted = new Set<HTMLElement>();
    // A missing context or scene source is a CSS-only mode. Do not attach
    // states or write the WebGL marker until the optical pass has pixels to
    // sample.
    if (this.renderer && this.environmentSource) {
      document.querySelectorAll<HTMLElement>(".material-surface[data-liquid-enhanced='true']").forEach((surface) => {
        if (
          isVisible(surface) &&
          !surface.parentElement?.closest<HTMLElement>(".material-surface[data-liquid-enhanced='true']")
        ) {
          wanted.add(surface);
        }
      });
      document.querySelectorAll<HTMLElement>(LIQUID_CONTROL_SELECTOR).forEach((control) => {
        if (
          control.dataset.liquidControl === "true" &&
          isVisible(control) &&
          !control.closest<HTMLElement>(".material-surface[data-liquid-enhanced='true']")
        ) {
          wanted.add(control);
        }
      });
    }

    this.surfaces.forEach((state, surface) => {
      if (wanted.has(surface)) return;
      state.resizeObserver?.disconnect();
      surface.removeAttribute("data-liquid-renderer");
      this.surfaces.delete(surface);
    });
    wanted.forEach((surface) => {
      if (!this.surfaces.has(surface)) this.attach(surface);
    });
    const active = Boolean(this.renderer && this.surfaces.size > 0);
    document.documentElement.dataset.liquidRenderer = active ? "webgl1" : "css";
    if (this.renderer) this.renderer.canvas.hidden = !active;
    this.dirty = true;
  }

  private attach(surface: HTMLElement): void {
    const state: SurfaceState = {
      surface,
      width: 0,
      height: 0,
      dpr: 1,
      radius: 12,
      pointerX: 0,
      pointerY: 0,
      pointerStrength: 0,
      pointerTarget: 0,
      pressed: false,
      pressProgress: 0
    };
    surface.dataset.liquidRenderer = "webgl1";
    state.resizeObserver = typeof ResizeObserver === "function"
      ? new ResizeObserver(() => { this.resize(state); this.dirty = true; this.ensureFrame(); })
      : undefined;
    state.resizeObserver?.observe(surface);
    this.surfaces.set(surface, state);
    this.resize(state);
  }

  private resize(state: SurfaceState): void {
    const rect = state.surface.getBoundingClientRect();
    state.dpr = Math.min(window.devicePixelRatio || 1, 2);
    state.width = Math.max(1, rect.width);
    state.height = Math.max(1, rect.height);
    state.radius = numericRadius(state.surface, state.width, state.height);
  }

  private ensureFrame(): void {
    if (this.destroyed || !this.renderer || this.animationFrame !== undefined) return;
    this.animationFrame = window.requestAnimationFrame(this.tick);
  }

  private render(state: SurfaceState): void {
    const renderer = this.renderer;
    if (!renderer || !isVisible(state.surface)) return;
    const gl = renderer.gl;
    const rect = state.surface.getBoundingClientRect();
    const dpr = renderer.dpr;
    const x = Math.max(0, Math.floor(rect.left * dpr));
    const y = Math.max(0, Math.floor(renderer.height - rect.bottom * dpr));
    const width = Math.min(renderer.width - x, Math.max(1, Math.ceil(rect.width * dpr)));
    const height = Math.min(renderer.height - y, Math.max(1, Math.ceil(rect.height * dpr)));
    if (width <= 0 || height <= 0) return;
    gl.scissor(x, y, width, height);
    gl.useProgram(renderer.program);
    gl.bindBuffer(gl.ARRAY_BUFFER, renderer.buffer);
    const position = gl.getAttribLocation(renderer.program, "aPosition");
    gl.enableVertexAttribArray(position);
    gl.vertexAttribPointer(position, 2, gl.FLOAT, false, 0, 0);
    const set2 = (name: string, a: number, b: number): void => gl.uniform2f(renderer.locations[name], a, b);
    const set1 = (name: string, value: number): void => gl.uniform1f(renderer.locations[name], value);
    const computed = getComputedStyle(state.surface);
    const transmission = Math.min(1, Math.max(0, cssNumber(
      computed,
      "--cz-material-transmission-body",
      cssNumber(computed, "--cz-material-transmission", 0)
    )));
    const edgeTransmission = Math.min(1, Math.max(0, cssNumber(
      computed,
      "--cz-material-transmission-edge",
      transmission
    )));
    const blur = Math.max(0, cssNumber(computed, "--cz-material-blur", 0));
    const refractionOffset = Math.max(0, cssNumber(computed, "--cz-material-refraction-offset", 0));
    const dispersionAlpha = Math.min(1, Math.max(0, cssNumber(computed, "--cz-material-dispersion-alpha", 0)));
    const opticalOpacity = Math.min(.46, Math.max(0, Math.max(transmission, edgeTransmission * .72)));
    const dispersionEnabled = state.surface.dataset.surfaceDispersion === "true" ||
      state.surface.dataset.controlDispersion === "true";
    const dispersion = dispersionEnabled ? dispersionAlpha : 0;
    gl.uniform1i(renderer.locations.uBackdrop, 0);
    set1("uHasBackdrop", renderer.hasBackdrop ? 1 : 0);
    set2("uSize", state.width * dpr, state.height * dpr);
    set2("uOrigin", rect.left * dpr, rect.top * dpr);
    set2("uViewport", renderer.width, renderer.height);
    set2("uPointer", state.pointerStrength > .001 ? state.pointerX : state.width * dpr * .5, state.pointerStrength > .001 ? state.pointerY : state.height * dpr * .35);
    set1("uRadius", state.radius * dpr);
    set1("uOpacity", opticalOpacity);
    set1("uBlur", blur * dpr);
    // The recipe's blur and refraction offset are CSS lengths. A half-blur
    // refraction gives the reference 12px height for the 24px Liquid recipe,
    // while reduced-effects recipes resolve both values to zero.
    set1("uRefraction", Math.max(refractionOffset, blur * .5) * dpr);
    set1("uDispersion", dispersion);
    set1("uPointerStrength", state.pointerStrength);
    set1("uPressed", state.pressProgress);
    set1("uTheme", document.documentElement.dataset.theme === "dark" ? 1 : 0);
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
  }

  private readonly tick = (time = 0): void => {
    this.animationFrame = undefined;
    if (this.destroyed || !this.renderer) return;
    const delta = this.lastTime > 0 ? Math.min(64, Math.max(0, time - this.lastTime)) : 16;
    this.lastTime = time;
    const pointerStep = 1 - Math.exp(-delta / 115);
    const pressStep = 1 - Math.exp(-delta / 72);
    const renderer = this.renderer;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const width = Math.max(1, Math.round(window.innerWidth * dpr));
    const height = Math.max(1, Math.round(window.innerHeight * dpr));
    if (renderer.width !== width || renderer.height !== height || renderer.dpr !== dpr) {
      renderer.width = width;
      renderer.height = height;
      renderer.dpr = dpr;
      renderer.canvas.width = width;
      renderer.canvas.height = height;
      this.dirty = true;
    }
    const gl = renderer.gl;
    this.uploadEnvironment(renderer);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, renderer.backdropTexture);
    gl.viewport(0, 0, width, height);
    gl.disable(gl.SCISSOR_TEST);
    gl.disable(gl.BLEND);
    gl.clearColor(0, 0, 0, 0);
    gl.clear(gl.COLOR_BUFFER_BIT);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);
    gl.enable(gl.SCISSOR_TEST);

    let moving = this.dirty;
    this.surfaces.forEach((state) => {
      state.pointerStrength += (state.pointerTarget - state.pointerStrength) * pointerStep;
      state.pressProgress += ((state.pressed ? 1 : 0) - state.pressProgress) * pressStep;
      if (Math.abs(state.pointerTarget - state.pointerStrength) > .002 || Math.abs((state.pressed ? 1 : 0) - state.pressProgress) > .002) moving = true;
      this.render(state);
    });
    this.dirty = false;
    if (moving && this.surfaces.size > 0) this.ensureFrame();
  };

  dispose(): void {
    this.destroyed = true;
    this.unbindEnvironmentSource();
    this.observer.disconnect();
    if (this.refreshFrame !== undefined) window.cancelAnimationFrame(this.refreshFrame);
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
      this.renderer.canvas.removeEventListener("webglcontextlost", this.onContextLost);
      this.renderer.canvas.removeEventListener("webglcontextrestored", this.onContextRestored);
      this.renderer.gl.deleteBuffer(this.renderer.buffer);
      this.renderer.gl.deleteTexture(this.renderer.backdropTexture);
      this.renderer.gl.deleteProgram(this.renderer.program);
      this.renderer.canvas.remove();
    }
    if (this.lostCanvas) {
      this.lostCanvas.removeEventListener("webglcontextlost", this.onContextLost);
      this.lostCanvas.removeEventListener("webglcontextrestored", this.onContextRestored);
      this.lostCanvas.remove();
      this.lostCanvas = undefined;
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

/** Configure a real scene texture before or after renderer installation. */
export function setLiquidGlassEnvironment(source: LiquidGlassEnvironmentSource | null): void {
  configuredEnvironmentSource = source;
  activeRenderer?.setEnvironmentSource(source);
}

export function disposeLiquidGlassRenderer(): void {
  activeRenderer?.dispose();
  activeRenderer = undefined;
}
