import * as React from "react";

import { cn } from "../../lib/utils";

export interface TextareaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement> {}

const Textarea = React.forwardRef<HTMLTextAreaElement, TextareaProps>(({ className, ...props }, ref) => {
  return <textarea ref={ref} className={cn("dashboardControl", className)} {...props} />;
});
Textarea.displayName = "Textarea";

export { Textarea };

