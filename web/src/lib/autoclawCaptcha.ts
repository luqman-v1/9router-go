// Aliyun captcha proof for AutoClaw's Z.ai OAuth. The desktop client runs the
// same SDK in a hidden webview; its verification can be silent (risk-based),
// so a proof may be produced without the user ever seeing a slider.

export interface AutoclawCaptchaConfig {
  enabled: boolean
  region: string
  prefix: string
  sceneId: string
}

interface AliyunWindow extends Window {
  AliyunCaptchaConfig?: Record<string, string>
  initAliyunCaptcha?: (options: Record<string, unknown>) => void
}

interface CaptchaInstance {
  destroy?: () => void
}

const SDK_URL = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js'
const CAPTCHA_TIMEOUT_MS = 30000
let sdkPromise: Promise<void> | null = null

function loadSdk(): Promise<void> {
  if (sdkPromise) return sdkPromise
  if (typeof window === 'undefined') return Promise.reject(new Error('captcha_sdk_unavailable'))

  // Promise.withResolvers is not in this project's TS lib target.
  return new Promise<void>((resolve, reject) => {
    const w = window as unknown as AliyunWindow
    if (typeof w.initAliyunCaptcha === 'function') {
      resolve()
      return
    }
    const script = document.createElement('script')
    script.src = SDK_URL
    script.async = true
    script.onload = () => {
      if (typeof w.initAliyunCaptcha === 'function') resolve()
      else reject(new Error('captcha_sdk_missing'))
    }
    script.onerror = () => reject(new Error('captcha_sdk_load_failed'))
    document.head.appendChild(script)
  })
}

// The SDK appends its popup overlay to document.body, outside our host.
// Without destroy() and DOM cleanup the "verifying" widget stays visible
// after the proof has been accepted.
function closeCaptchaUi(instance: CaptchaInstance | null, element: HTMLElement, trigger: HTMLElement): void {
  instance?.destroy?.()
  document.querySelectorAll('.aliyun-captcha, .aliyunCaptcha-top').forEach((node) => node.remove())
  element.remove()
  trigger.remove()
}

/**
 * Run the Aliyun captcha challenge and resolve with the proof parameter that
 * AutoClaw's identity gateway expects as `ali_captcha_verify_param`.
 *
 * The SDK can settle silently (risk-based V3) or show a slider. Either way
 * it returns a proof. The host must be visible in the DOM for the SDK to
 * mount its popup surface.
 */
export async function runAutoclawCaptcha(
  config: AutoclawCaptchaConfig,
  host: HTMLElement
): Promise<string> {
  if (!config.enabled) throw new Error('AutoClaw captcha is not enabled')
  await loadSdk()
  const w = window as unknown as AliyunWindow
  w.AliyunCaptchaConfig = { region: config.region, prefix: config.prefix }

  const element = document.createElement('div')
  element.id = 'autoclaw-captcha-element'
  const trigger = document.createElement('button')
  trigger.id = 'autoclaw-captcha-trigger'
  trigger.type = 'button'
  trigger.textContent = 'Verify'
  host.append(element, trigger)

  let instance: CaptchaInstance | null = null
  // Promise.withResolvers is not in this project's TS lib target.
  let resolveProof!: (value: string) => void
  let rejectProof!: (reason: Error) => void
  const promise = new Promise<string>((resolve, reject) => {
    resolveProof = resolve
    rejectProof = reject
  })
  const timer = setTimeout(() => finish(null, new Error('captcha_timeout')), CAPTCHA_TIMEOUT_MS)

  function finish(proof: string | null, error?: Error): void {
    clearTimeout(timer)
    closeCaptchaUi(instance, element, trigger)
    if (proof) resolveProof(proof)
    else rejectProof(error ?? new Error('captcha_cancelled'))
  }

  try {
    w.initAliyunCaptcha?.({
      SceneId: config.sceneId,
      mode: 'popup',
      element: '#autoclaw-captcha-element',
      button: '#autoclaw-captcha-trigger',
      captchaVerifyCallback: (value: unknown) => {
        const proof = typeof value === 'string' ? value : JSON.stringify(value)
        finish(proof)
        // The SDK expects a promise that resolves after the business
        // request completes. Login continues via the returned proof.
        return Promise.resolve()
      },
      onError: () => finish(null, new Error('captcha_sdk_init_failed')),
      getInstance: (sdkInstance: CaptchaInstance) => {
        instance = sdkInstance
        // The desktop client clicks the hidden trigger after the SDK
        // reports an instance. A silent challenge resolves the callback
        // without ever showing a slider.
        setTimeout(() => trigger.click(), 0)
      },
    })
  } catch (err) {
    finish(null, err instanceof Error ? err : new Error(String(err)))
  }
  return promise
}
