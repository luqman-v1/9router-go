<script lang="ts">
  // Port of upstream CustomJsonFields.js (decolua/9router#4929): the request
  // and response mapping of a custom JSON node, shared by the add and edit
  // dialogs.
  import Button from '../../lib/ui/Button.svelte'
  import Input from '../../lib/ui/Input.svelte'
  import {
    CUSTOM_JSON_DEFAULT_TEMPLATE,
    CUSTOM_JSON_METHODS,
    switchCustomJsonTemplateMode,
    type CustomJsonForm,
  } from '../../lib/customJsonForm'

  interface Props {
    value: CustomJsonForm
    /** Prefix for element ids, so two dialogs never share one. */
    idPrefix?: string
  }

  let { value = $bindable(), idPrefix = 'custom-json' }: Props = $props()

  let modeError = $state('')
  let nextFieldId = 0

  const selectCls =
    'w-full py-2.5 px-3 pr-10 text-sm text-text-main bg-surface-2 border border-transparent rounded-[10px] appearance-none focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500/40 transition-all duration-150'
  const textareaCls =
    'w-full rounded-[10px] border border-transparent bg-surface-2 p-3 font-mono text-sm text-text-main focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500/40'
  const defaultTemplateText = JSON.stringify(CUSTOM_JSON_DEFAULT_TEMPLATE, null, 2)

  function changeMode(mode: CustomJsonForm['templateMode']) {
    modeError = ''
    try {
      value = switchCustomJsonTemplateMode(value, mode)
    } catch (err) {
      modeError = err instanceof Error ? err.message : String(err)
    }
  }

  function addField() {
    value.extraFields = [...value.extraFields, { id: `new-${nextFieldId++}`, name: '', type: 'text', value: '' }]
  }

  function removeField(id: string) {
    value.extraFields = value.extraFields.filter((field) => field.id !== id)
  }
</script>

<div class="flex flex-col gap-4">
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <div>
      <label for="{idPrefix}-method" class="mb-1.5 block text-sm font-medium text-text-main">HTTP Method</label>
      <select id="{idPrefix}-method" bind:value={value.method} class={selectCls}>
        {#each CUSTOM_JSON_METHODS as method (method)}
          <option value={method}>{method}</option>
        {/each}
      </select>
    </div>
    <div>
      <label for="{idPrefix}-auth" class="mb-1.5 block text-sm font-medium text-text-main">Authentication</label>
      <select id="{idPrefix}-auth" bind:value={value.authType} class={selectCls}>
        <option value="bearer">Bearer token</option>
        <option value="api-key">API key in header</option>
        <option value="none">None</option>
      </select>
    </div>
  </div>
  {#if value.authType === 'api-key'}
    <Input label="API Key Header" bind:value={value.authHeader} placeholder="X-API-Key" inputClass="font-mono" />
  {/if}

  <fieldset class="flex min-w-0 flex-col gap-3">
    <legend class="mb-2 text-sm font-medium text-text-main">Request JSON Template</legend>
    <div class="flex gap-2">
      <Button size="sm" variant={value.templateMode === 'fields' ? 'primary' : 'ghost'} onclick={() => changeMode('fields')}>
        Field editor
      </Button>
      <Button size="sm" variant={value.templateMode === 'json' ? 'primary' : 'ghost'} onclick={() => changeMode('json')}>
        Full JSON (advanced)
      </Button>
    </div>
    {#if value.templateMode === 'fields'}
      <p class="text-xs text-text-muted">Sends model and messages by default. Extra fields are optional.</p>
      <pre aria-label="Default request template" class="{textareaCls} overflow-x-auto">{defaultTemplateText}</pre>
      {#each value.extraFields as field, index (field.id)}
        <div class="flex flex-col gap-3 rounded-[10px] border border-border-subtle p-3">
          <div class="flex items-center justify-between gap-3">
            <span class="text-sm text-text-main">Extra field {index + 1} <span class="text-text-muted">(optional)</span></span>
            <Button size="sm" variant="ghost" onclick={() => removeField(field.id)}>Delete</Button>
          </div>
          <div class="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
            <Input label="Field name" bind:value={field.name} placeholder="e.g. project_id" inputClass="font-mono" />
            <div>
              <label for="{idPrefix}-type-{field.id}" class="mb-1.5 block text-sm font-medium text-text-main">Value type</label>
              <select id="{idPrefix}-type-{field.id}" bind:value={field.type} class={selectCls}>
                <option value="text">Text / placeholder</option>
                <option value="json">JSON (number, boolean, array or object)</option>
              </select>
            </div>
          </div>
          <Input label="Field value" bind:value={field.value} placeholder={'e.g. demo-project or {{prompt}}'} inputClass="font-mono" />
        </div>
      {/each}
      <div>
        <Button size="sm" variant="secondary" icon="add" onclick={addField}>Add field</Button>
      </div>
    {:else}
      <label for="{idPrefix}-template" class="sr-only">Request JSON Template</label>
      <textarea id="{idPrefix}-template" rows="8" spellcheck="false" bind:value={value.requestTemplateText} class={textareaCls}></textarea>
      <p class="text-xs text-text-muted">Full JSON mode lets you change or remove default fields. Existing custom templates are preserved.</p>
    {/if}
    {#if modeError}
      <p role="alert" class="text-sm text-red-500">{modeError}</p>
    {/if}
    <p class="text-xs text-text-muted">
      {'Use {{prompt}} for the latest user text, {{messages}} for chat history, {{model}} for the model ID, or {{request.field}} for a supplied request field. A whole placeholder preserves arrays and objects.'}
    </p>
  </fieldset>

  <Input
    label="Response Field Path"
    bind:value={value.responsePath}
    placeholder="data.answer"
    hint="Examples: answer, data.answer, results[0].text. Use $ to return the entire JSON value."
    inputClass="font-mono"
  />

  <details>
    <summary class="cursor-pointer text-sm text-text-main">Custom Headers</summary>
    <label for="{idPrefix}-headers" class="sr-only">Custom Headers JSON</label>
    <textarea id="{idPrefix}-headers" rows="3" spellcheck="false" bind:value={value.headersText} class="{textareaCls} mt-2"></textarea>
    <p class="mt-1 text-xs text-text-muted">Store the secret API key in the connection. These headers are saved as provider settings.</p>
  </details>

  <p class="text-xs text-text-muted">
    The endpoint returns JSON. Streaming clients receive the reply after the complete response arrives. Test sends a real request with “ping” as the prompt.
  </p>
</div>
