import React from 'react';

interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'outline';
  loading?: boolean;
}

export const Button: React.FC<ButtonProps> = ({
  children,
  variant = 'primary',
  loading = false,
  disabled,
  className = '',
  ...props
}) => {
  return (
    <button
      className={`btn btn-${variant} ${loading ? 'btn-loading' : ''} ${className}`}
      disabled={disabled || loading}
      {...props}
    >
      {loading ? <span className="spinner" aria-hidden="true" /> : null}
      <span>{children}</span>
    </button>
  );
};
