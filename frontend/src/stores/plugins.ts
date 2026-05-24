import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '../api'
import type { Plugin } from '../types'

export const usePluginStore = defineStore('plugins', () => {
  const list = ref<Plugin[]>([])
  const deleted = ref<Plugin[]>([])
  const current = ref<Plugin | null>(null)

  async function loadList() {
    list.value = await api.listPlugins()
  }

  async function loadDeleted() {
    deleted.value = await api.listDeletedPlugins()
  }

  async function loadPlugin(name: string) {
    current.value = await api.getPlugin(name)
    return current.value
  }

  async function refreshCurrent() {
    if (!current.value) return null
    return loadPlugin(current.value.name)
  }

  async function createPlugin(data: Partial<Plugin>) {
    const p = await api.createPlugin(data)
    list.value = [...list.value, p]
    return p
  }

  async function updatePlugin(name: string, data: Partial<Plugin>) {
    const p = await api.updatePlugin(name, data)
    list.value = list.value.map(item => item.name === name ? p : item)
    if (current.value?.name === name) current.value = p
    return p
  }

  async function deletePlugin(name: string) {
    await api.deletePlugin(name)
    list.value = list.value.filter(p => p.name !== name)
    if (current.value?.name === name) current.value = null
  }

  async function restorePlugin(name: string) {
    const p = await api.restorePlugin(name)
    list.value = [...list.value, p]
    deleted.value = deleted.value.filter(d => d.name !== name)
    return p
  }

  return {
    list, deleted, current,
    loadList, loadDeleted, loadPlugin, refreshCurrent,
    createPlugin, updatePlugin, deletePlugin, restorePlugin,
  }
})
