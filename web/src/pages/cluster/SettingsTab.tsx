import type { VClusterView } from '../../api/types';
import { AdminSettingsForms } from './AdminSettingsForms';
import { DangerZone } from './DangerZone';

interface Props {
  vc: VClusterView;
  isAdmin: boolean;
  canDelete: boolean;
  onSaved: () => void;
}

// 设置 Tab：管理员看到三个表单加危险操作；所有者（非管理员）只看到危险操作。
export function SettingsTab({ vc, isAdmin, canDelete, onSaved }: Props) {
  return (
    <>
      {isAdmin && <AdminSettingsForms vc={vc} onSaved={onSaved} />}
      {canDelete && <DangerZone vc={vc} />}
    </>
  );
}
