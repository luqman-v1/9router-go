<script lang="ts">
  import Modal from '../../lib/ui/Modal.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Badge from '../../lib/ui/Badge.svelte'

  interface Props {
    isOpen: boolean
    onClose: () => void
    comboName: string
    currentPolicy?: string
    currentThreshold?: number
    onSave: (cfg: { smartPolicy: string; sessionSticky: boolean; confidenceThreshold: number }) => void
  }

  let { isOpen, onClose, comboName, currentPolicy = 'smart', currentThreshold = 0.8, onSave }: Props = $props()

  const QUICK_THRESHOLDS = [60, 70, 75, 80, 85, 90]

  let selectedPolicy = $state('smart')
  let threshold = $state(80)

  $effect(() => {
    if (isOpen) {
      let initial = currentPolicy || 'smart'
      if (initial.startsWith('smart-switch')) initial = 'smart'
      selectedPolicy = initial
      const pct = Math.round((currentThreshold || 0.8) * (currentThreshold <= 1 ? 100 : 1))
      threshold = pct || 80
    }
  })

  function handleSave() {
    const finalThreshold =
      selectedPolicy === 'custom' ? threshold / 100 : selectedPolicy === 'smart' ? 0.8 : 0
    onSave({
      smartPolicy: selectedPolicy,
      sessionSticky: selectedPolicy !== 'per-turn',
      confidenceThreshold: finalThreshold,
    })
    onClose()
  }

  const policies = $derived([
    {
      id: 'smart',
      title: 'Smart Switch',
      badge: 'Adaptive ≥80%',
      badgeVariant: 'success',
      icon: 'auto_mode',
      recommended: true,
      desc: 'Preserves warm prompt cache across turns. Seamlessly switches models if the conversation topic shifts with high confidence (≥80%).',
    },
    {
      id: 'strict',
      title: 'Strict Lock',
      badge: '1 Task = 1 Session',
      badgeVariant: 'default',
      icon: 'lock',
      desc: 'Evaluates System One on turn 1, then locks that model for the entire session. Adds +0ms latency and zero extra router calls (100% prompt cache reuse).',
    },
    {
      id: 'per-turn',
      title: 'Per-Turn',
      badge: 'Dynamic',
      badgeVariant: 'default',
      icon: 'refresh',
      desc: 'Evaluates System One independently on every prompt without session stickiness.',
    },
    {
      id: 'custom',
      title: 'Custom Threshold',
      badge: `Adaptive ≥${threshold}%`,
      badgeVariant: 'default',
      icon: 'tune',
      desc: 'Configure your own confidence threshold required to trigger a model switch.',
    },
  ])
</script>

<Modal {isOpen} {onClose} title="Router Policy — {comboName || 'Combo'}" size="lg">
  <div class="flex flex-col gap-4">
    <p class="text-xs text-text-muted">
      Choose how the System One router manages model selection across conversation turns:
    </p>

    <div class="flex flex-col gap-2.5">
      {#each policies as p (p.id)}
        {@const isSelected = selectedPolicy === p.id}
        <div
          role="radio"
          aria-checked={isSelected}
          tabindex="0"
          onclick={() => (selectedPolicy = p.id)}
          onkeydown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault()
              selectedPolicy = p.id
            }
          }}
          class={`relative flex flex-col rounded-xl border p-3 cursor-pointer transition-all ${
            isSelected
              ? 'border-primary bg-primary/[0.04] shadow-sm'
              : 'border-border/60 hover:border-border hover:bg-black/[0.01] dark:hover:bg-white/[0.01]'
          }`}
        >
          <div class="flex items-center justify-between gap-2">
            <div class="flex items-center gap-2">
              <span
                class={`size-4 rounded-full border flex items-center justify-center shrink-0 transition-all ${
                  isSelected ? 'border-primary bg-primary text-white' : 'border-border-subtle bg-surface'
                }`}
              >
                {#if isSelected}<span class="size-1.5 rounded-full bg-white"></span>{/if}
              </span>
              <span class="size-5 rounded flex items-center justify-center shrink-0 text-text-muted">
                <span class="material-symbols-outlined text-[15px] leading-none select-none inline-block">{p.icon}</span>
              </span>
              <span class="text-sm font-medium text-text-main">{p.title}</span>
              {#if p.recommended}
                <span class="text-[10px] uppercase tracking-wide font-semibold text-primary bg-primary/10 px-1.5 py-0.5 rounded">
                  Recommended
                </span>
              {/if}
            </div>
            <Badge variant={p.badgeVariant as 'success' | 'default'} size="sm">{p.badge}</Badge>
          </div>

          <p class="text-xs text-text-muted mt-1.5 pl-6 leading-relaxed">{p.desc}</p>

          {#if p.id === 'custom' && isSelected}
            <div
              class="mt-3 pl-6 pt-3 border-t border-border/40 flex flex-col gap-2.5"
              onclick={(e) => e.stopPropagation()}
              onkeydown={(e) => e.stopPropagation()}
            >
              <div class="flex items-center justify-between text-xs">
                <span class="font-medium text-text-muted">Switch Confidence Threshold:</span>
                <span class="font-mono font-semibold text-primary text-sm">{threshold}%</span>
              </div>

              <input
                type="range"
                min="50"
                max="95"
                step="1"
                bind:value={threshold}
                class="w-full accent-primary cursor-pointer h-1.5 bg-black/10 dark:bg-white/10 rounded-lg appearance-none"
                aria-label="Switch confidence threshold percent"
              />

              <div class="flex items-center gap-1.5 pt-1">
                <span class="text-[10px] text-text-muted mr-1">Presets:</span>
                {#each QUICK_THRESHOLDS as pct (pct)}
                  <button
                    type="button"
                    onclick={() => (threshold = pct)}
                    class={`px-2 py-0.5 rounded text-[11px] font-mono transition-colors cursor-pointer ${
                      threshold === pct
                        ? 'bg-primary text-white font-semibold'
                        : 'bg-black/5 dark:bg-white/5 text-text-muted hover:text-text-main'
                    }`}
                  >
                    {pct}%
                  </button>
                {/each}
              </div>
            </div>
          {/if}
        </div>
      {/each}
    </div>

    <div class="rounded-xl border border-primary/20 bg-primary/[0.03] p-3 text-xs flex items-start gap-2.5">
      <span class="material-symbols-outlined text-primary text-[18px] shrink-0 mt-0.5">bolt</span>
      <div class="text-text-muted leading-relaxed">
        <span class="font-medium text-text-main">Prompt Cache Benefits: </span>
        Retaining the same model across session turns keeps upstream prompt caches warm, reducing
        input token costs by up to <strong class="text-text-main">90%</strong> and cutting latency
        in half (Claude Prompt Cache, DeepSeek KV-cache, Gemini Implicit Cache).
      </div>
    </div>

    <div class="flex items-center justify-end gap-2 pt-2 border-t border-border/40">
      <Button variant="ghost" size="sm" onclick={onClose}>Cancel</Button>
      <Button variant="primary" size="sm" onclick={handleSave}>Save Policy</Button>
    </div>
  </div>
</Modal>
