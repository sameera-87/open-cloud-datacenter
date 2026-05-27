import type { ReactNode } from 'react';
import {
  Button,
  Menu,
  MenuList,
  MenuPopover,
  MenuTrigger,
} from '@fluentui/react-components';
import { MoreHorizontal20Regular } from '@fluentui/react-icons';

/**
 * Per-row 3-dots actions menu for list tables (VMs, Bastions, Clusters,
 * VNets, NSGs). The wrapper span stops click propagation so the row's
 * navigate-to-detail handler doesn't fire when the user opens the menu or
 * picks an item — every list page currently makes the whole row clickable
 * (rowClickable in useListPageStyles), and we want the kebab to be its own
 * interaction surface.
 *
 * Children are expected to be Fluent MenuItem elements; the caller wires
 * each item's onClick to its own mutation / confirm flow.
 */
export function RowActionsMenu({ children }: { children: ReactNode }) {
  return (
    <span onClick={(e) => e.stopPropagation()}>
      <Menu>
        <MenuTrigger disableButtonEnhancement>
          <Button
            appearance="subtle"
            icon={<MoreHorizontal20Regular />}
            aria-label="Row actions"
            size="small"
          />
        </MenuTrigger>
        <MenuPopover>
          <MenuList>{children}</MenuList>
        </MenuPopover>
      </Menu>
    </span>
  );
}
