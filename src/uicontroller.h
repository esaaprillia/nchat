// uicontroller.h
//
// Copyright (c) 2019-2023 Kristofer Berggren
// All rights reserved.
//
// nchat is distributed under the MIT license, see LICENSE for details.

#pragma once

#include <ncurses.h>

#define KEY_FOCUS_IN 1001
#define KEY_FOCUS_OUT 1002

class UiController
{
public:
  UiController();
  virtual ~UiController();

  void Init();
  void Cleanup();

  static wint_t GetKey(int p_TimeOutMs);

private:
};
