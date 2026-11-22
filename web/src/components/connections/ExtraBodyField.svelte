<script lang="ts">
  import { EXTRA_BODY_EXAMPLE, EXTRA_BODY_REASONING_KEYS, EXTRA_BODY_SAMPLING_KEYS } from '../../lib/extraBody'

  interface Props {
    value: string
    error: string
  }

  let { value = $bindable(''), error }: Props = $props()

  const groups = [
    { label: 'Sampling', keys: EXTRA_BODY_SAMPLING_KEYS },
    { label: 'Reasoning', keys: EXTRA_BODY_REASONING_KEYS },
  ]
</script>

<div>
  <label class="text-xs text-text-muted mb-1 block" for="add-custom-model-extra-body">
    Custom request body (JSON, optional)
  </label>
  <textarea
    id="add-custom-model-extra-body"
    rows="4"
    bind:value
    placeholder={EXTRA_BODY_EXAMPLE}
    aria-describedby={error ? 'add-custom-model-extra-body-error' : undefined}
    aria-invalid={error ? 'true' : undefined}
    class="w-full px-3 py-2 text-sm font-mono border border-border rounded-lg bg-background focus:outline-none focus:border-primary"
  ></textarea>
  {#if error}
    <p id="add-custom-model-extra-body-error" class="text-xs text-red-600 dark:text-red-400 mt-1" role="alert">
      {error}
    </p>
  {/if}
  <div class="text-xs text-text-muted mt-1 space-y-1">
    <p>Merged into every request for this model; your values win. Only these parameters are accepted:</p>
    {#each groups as group (group.label)}
      <p class="flex flex-wrap items-center gap-1">
        <span class="text-text-subtle">{group.label}:</span>
        {#each group.keys as key (key)}
          <code class="font-mono bg-sidebar px-1 rounded">{key}</code>
        {/each}
      </p>
    {/each}
    <p>Other keys are rejected on save and ignored at runtime.</p>
    <p>Example: <code class="font-mono bg-sidebar px-1 rounded">{EXTRA_BODY_EXAMPLE}</code></p>
    <p>
      <a
        href="https://openrouter.ai/docs/api-reference/parameters"
        target="_blank"
        rel="noopener noreferrer"
        class="text-primary hover:underline">OpenRouter parameters guide</a>
      ·
      <a
        href="https://openrouter.ai/docs/use-cases/reasoning-tokens"
        target="_blank"
        rel="noopener noreferrer"
        class="text-primary hover:underline">Reasoning tokens guide</a>
    </p>
  </div>
</div>
