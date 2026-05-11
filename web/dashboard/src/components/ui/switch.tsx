import * as React from "react";

import { cn } from "../../lib/utils";

export interface SwitchProps extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "onChange"> {
  checked?: boolean;
  onCheckedChange?: (checked: boolean) => void;
}

const Switch = React.forwardRef<HTMLButtonElement, SwitchProps>(
  ({ checked = false, onCheckedChange, className, disabled, ...props }, ref) => {
    return (
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        ref={ref}
        className={cn("dashboardSwitch", checked && "on", className)}
        onClick={() => {
          if (!disabled) onCheckedChange?.(!checked);
        }}
        {...props}
      >
        <span />
      </button>
    );
  }
);
Switch.displayName = "Switch";

export { Switch };
