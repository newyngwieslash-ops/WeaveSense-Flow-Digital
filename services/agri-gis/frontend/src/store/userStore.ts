import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { getProjectUsers } from '@/api/index'
import type { ProjectUser } from '@/types/workbench'
import { legacyDemoEnabled, selectedDemoIdentity, explicitDemoContext } from '@/security/legacyIdentity'

export const useUserStore = defineStore('user', () => {
  const usersRef = ref<ProjectUser[]>([])
  const currentUserCodeRef = ref<string>('')
  const loadingRef = ref<boolean>(false)
  const initializedRef = ref<boolean>(false)

  const currentUserComputed = computed<ProjectUser | null>(() => (
    selectedDemoIdentity(legacyDemoEnabled(), usersRef.value, currentUserCodeRef.value)
  ))

  /**
   * 加载服务端项目用户和角色能力。
   * Args:
   *   projectCode: 项目编号。
   * Returns:
   *   Promise<void>: 用户目录初始化完成后结束。
   */
  const initialize = async (projectCode: string = ''): Promise<void> => {
    if (!explicitDemoContext(legacyDemoEnabled(), projectCode)) {
      usersRef.value = []
      currentUserCodeRef.value = ''
      return
    }
    if (initializedRef.value || loadingRef.value) return
    loadingRef.value = true
    try {
      const result = await getProjectUsers(projectCode)
      usersRef.value = result.users
      initializedRef.value = true
    } finally {
      loadingRef.value = false
    }
  }

  /**
   * 切换当前工作台身份。
   * Args:
   *   userCode: 项目用户稳定编码。
   * Returns:
   *   void: 无返回值。
   */
  const setCurrentUser = (userCode: string): void => {
    if (!legacyDemoEnabled()) return
    if (!usersRef.value.some((user) => user.user_code === userCode)) return
    currentUserCodeRef.value = userCode
  }

  /**
   * 判断当前项目用户是否具备目标业务能力。
   * Args:
   *   capability: 后端返回的能力编码。
   * Returns:
   *   boolean: 具备能力时返回 true。
   */
  const hasCapability = (capability: string): boolean => (
    currentUserComputed.value?.capabilities.includes(capability) ?? false
  )

  return {
    usersRef,
    currentUserCodeRef,
    currentUserComputed,
    loadingRef,
    initializedRef,
    initialize,
    setCurrentUser,
    hasCapability,
  }
})
