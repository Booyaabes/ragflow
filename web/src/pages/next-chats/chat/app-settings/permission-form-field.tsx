import { SelectWithSearch } from '@/components/originui/select-with-search';
import { RAGFlowFormItem } from '@/components/ragflow-form';
import { PermissionRole } from '@/constants/permission';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

export function PermissionFormField({ name = 'permission' }: { name?: string }) {
  const { t } = useTranslation();
  const teamOptions = useMemo(() => {
    return Object.values(PermissionRole).map((x) => ({
      label: t('chat.' + x),
      value: x,
    }));
  }, [t]);

  return (
    <RAGFlowFormItem
      name={name}
      label={t('chat.permissions')}
      tooltip={t('chat.permissionsTip')}
      horizontal
    >
      <SelectWithSearch
        options={teamOptions}
        triggerClassName="w-full"
        testId="chat-settings-basic-permissions-select"
      ></SelectWithSearch>
    </RAGFlowFormItem>
  );
}
